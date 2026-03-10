package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fullstorydev/grpcurl"
	"github.com/golang/protobuf/jsonpb" //lint:ignore SA1019 needed for grpcurl API
	"github.com/golang/protobuf/proto"  //lint:ignore SA1019 needed for grpcurl API
	"github.com/jhump/protoreflect/desc" //lint:ignore SA1019 needed for grpcurl API
	"github.com/jhump/protoreflect/grpcreflect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

//go:embed static
var staticFS embed.FS

// connEntry holds a cached gRPC connection and its descriptor source.
type connEntry struct {
	conn     *grpc.ClientConn
	source   grpcurl.DescriptorSource
	target   string
	usedAt   time.Time
	protoDir string // temp dir for uploaded proto files; cleaned up on remove
}

type connStore struct {
	mu    sync.Mutex
	conns map[string]*connEntry
}

func newConnStore() *connStore {
	return &connStore{conns: make(map[string]*connEntry)}
}

func (s *connStore) get(target string) (*connEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.conns[target]
	if ok {
		e.usedAt = time.Now()
	}
	return e, ok
}

func (s *connStore) put(target string, e *connEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[target] = e
}

func (s *connStore) remove(target string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.conns[target]; ok {
		e.conn.Close()
		if e.protoDir != "" {
			os.RemoveAll(e.protoDir)
		}
		delete(s.conns, target)
	}
}

// updateSource replaces the descriptor source for an existing connection.
func (s *connStore) updateSource(target string, source grpcurl.DescriptorSource, protoDir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.conns[target]
	if !ok {
		return false
	}
	// Clean up old proto dir if any.
	if e.protoDir != "" {
		os.RemoveAll(e.protoDir)
	}
	e.source = source
	e.protoDir = protoDir
	e.usedAt = time.Now()
	return true
}

func (s *connStore) evictStale() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for k, e := range s.conns {
		if e.usedAt.Before(cutoff) {
			e.conn.Close()
			if e.protoDir != "" {
				os.RemoveAll(e.protoDir)
			}
			delete(s.conns, k)
		}
	}
}

var store = newConnStore()

func main() {
	addr := flag.String("listen", ":8080", "HTTP listen address")
	flag.Parse()

	go func() {
		for range time.Tick(time.Minute) {
			store.evictStale()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/connect", handleConnect)
	mux.HandleFunc("/api/disconnect", handleDisconnect)
	mux.HandleFunc("/api/services", handleServices)
	mux.HandleFunc("/api/methods", handleMethods)
	mux.HandleFunc("/api/describe", handleDescribe)
	mux.HandleFunc("/api/invoke", handleInvoke)
	mux.HandleFunc("/api/upload-protos", handleUploadProtos)

	webSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("failed to create sub filesystem: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webSub)))

	log.Printf("web-grpcurl listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

type connectRequest struct {
	Target   string `json:"target"`
	TLS      bool   `json:"tls"`
	Insecure bool   `json:"insecure"`
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func handleConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req connectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body: "+err.Error())
		return
	}
	if req.Target == "" {
		writeErr(w, 400, "target is required")
		return
	}

	store.remove(req.Target)

	// Use a timeout context only for the dial, not for the long-lived
	// reflection client/descriptor source (those must outlive this request).
	dialCtx, dialCancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer dialCancel()

	var creds credentials.TransportCredentials
	if req.TLS {
		tlsConf, err := grpcurl.ClientTLSConfig(req.Insecure, "", "", "")
		if err != nil {
			writeErr(w, 500, "TLS config error: "+err.Error())
			return
		}
		creds = credentials.NewTLS(tlsConf)
	}

	cc, err := grpcurl.BlockingDial(dialCtx, "", req.Target, creds)
	if err != nil {
		writeErr(w, 502, "failed to connect: "+err.Error())
		return
	}

	// Background context for the reflection client so it survives beyond this HTTP request.
	refClient := grpcreflect.NewClientAuto(context.Background(), cc)
	source := grpcurl.DescriptorSourceFromServer(context.Background(), refClient)

	store.put(req.Target, &connEntry{
		conn:   cc,
		source: source,
		target: req.Target,
		usedAt: time.Now(),
	})

	svcs, err := grpcurl.ListServices(source)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"connected": true,
			"services":  []string{},
			"warning":   "connected but reflection may not be supported: " + err.Error(),
		})
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"connected": true,
		"services":  svcs,
	})
}

func handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req struct {
		Target string `json:"target"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Target != "" {
		store.remove(req.Target)
	}
	writeJSON(w, 200, map[string]string{"status": "disconnected"})
}

func handleServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	target := r.URL.Query().Get("target")
	entry, ok := store.get(target)
	if !ok {
		writeErr(w, 400, "not connected to "+target)
		return
	}
	svcs, err := grpcurl.ListServices(entry.source)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"services": svcs})
}

func handleMethods(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	target := r.URL.Query().Get("target")
	service := r.URL.Query().Get("service")
	entry, ok := store.get(target)
	if !ok {
		writeErr(w, 400, "not connected to "+target)
		return
	}
	methods, err := grpcurl.ListMethods(entry.source, service)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]interface{}{"methods": methods})
}

func handleDescribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	target := r.URL.Query().Get("target")
	symbol := r.URL.Query().Get("symbol")
	entry, ok := store.get(target)
	if !ok {
		writeErr(w, 400, "not connected to "+target)
		return
	}

	dsc, err := entry.source.FindSymbol(symbol)
	if err != nil {
		writeErr(w, 404, "symbol not found: "+err.Error())
		return
	}

	txt, err := grpcurl.GetDescriptorText(dsc, entry.source)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}

	result := map[string]interface{}{
		"descriptor": txt,
	}

	if md, ok := dsc.(*desc.MethodDescriptor); ok {
		reqTemplate := grpcurl.MakeTemplate(md.GetInputType())
		marshaler := jsonpb.Marshaler{EmitDefaults: true, Indent: "  "}
		reqJSON, err := marshaler.MarshalToString(reqTemplate)
		if err == nil {
			result["request_template"] = reqJSON
		}
		result["request_type"] = md.GetInputType().GetFullyQualifiedName()
		result["response_type"] = md.GetOutputType().GetFullyQualifiedName()
		result["client_streaming"] = md.IsClientStreaming()
		result["server_streaming"] = md.IsServerStreaming()
	}

	writeJSON(w, 200, result)
}

type invokeRequest struct {
	Target  string            `json:"target"`
	Method  string            `json:"method"`
	Body    json.RawMessage   `json:"body"`
	Headers map[string]string `json:"headers"`
	Timeout int               `json:"timeout"`
}

type webEventHandler struct {
	formatter   grpcurl.Formatter
	responses   []json.RawMessage
	respHeaders metadata.MD
	trailers    metadata.MD
	stat        *status.Status
}

func (h *webEventHandler) OnResolveMethod(*desc.MethodDescriptor) {}
func (h *webEventHandler) OnSendHeaders(metadata.MD)              {}

func (h *webEventHandler) OnReceiveHeaders(md metadata.MD) {
	h.respHeaders = md
}

func (h *webEventHandler) OnReceiveResponse(msg proto.Message) {
	txt, err := h.formatter(msg)
	if err == nil {
		h.responses = append(h.responses, json.RawMessage(txt))
	}
}

func (h *webEventHandler) OnReceiveTrailers(stat *status.Status, md metadata.MD) {
	h.stat = stat
	h.trailers = md
}

func handleInvoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req invokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "invalid request body: "+err.Error())
		return
	}
	entry, ok := store.get(req.Target)
	if !ok {
		writeErr(w, 400, "not connected to "+req.Target)
		return
	}

	timeout := 30
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeout)*time.Second)
	defer cancel()

	var headers []string
	for k, v := range req.Headers {
		headers = append(headers, k+": "+v)
	}

	requestBody := string(req.Body)
	if requestBody == "" || requestBody == "null" {
		requestBody = "{}"
	}

	resolver := grpcurl.AnyResolverFromDescriptorSourceWithFallback(entry.source)
	formatter := grpcurl.NewJSONFormatter(true, resolver)
	parser := grpcurl.NewJSONRequestParser(strings.NewReader(requestBody), resolver)

	handler := &webEventHandler{formatter: formatter}

	err := grpcurl.InvokeRPC(ctx, entry.source, entry.conn, req.Method, headers, handler, parser.Next)

	result := map[string]interface{}{}

	if len(handler.responses) == 1 {
		result["response"] = handler.responses[0]
	} else if len(handler.responses) > 1 {
		result["responses"] = handler.responses
	}

	if handler.respHeaders != nil {
		result["response_headers"] = metadataToMap(handler.respHeaders)
	}
	if handler.trailers != nil {
		result["response_trailers"] = metadataToMap(handler.trailers)
	}

	if handler.stat != nil {
		result["status"] = map[string]interface{}{
			"code":    handler.stat.Code().String(),
			"message": handler.stat.Message(),
		}
		if handler.stat.Code() != codes.OK {
			result["error"] = fmt.Sprintf("RPC error: %s - %s", handler.stat.Code(), handler.stat.Message())
		}
	}

	if err != nil {
		if handler.stat == nil || handler.stat.Code() == codes.OK {
			result["error"] = err.Error()
		}
	}

	writeJSON(w, 200, result)
}

func metadataToMap(md metadata.MD) map[string]string {
	m := make(map[string]string, len(md))
	for k, vs := range md {
		m[k] = strings.Join(vs, ", ")
	}
	return m
}

func handleUploadProtos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}

	target := r.FormValue("target")
	entry, ok := store.get(target)
	if !ok {
		writeErr(w, 400, "not connected to "+target)
		return
	}
	_ = entry // just validating connection exists

	// 32 MB max upload
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, 400, "failed to parse upload: "+err.Error())
		return
	}

	// Create temp directory for uploaded files.
	tmpDir, err := os.MkdirTemp("", "web-grpcurl-protos-*")
	if err != nil {
		writeErr(w, 500, "failed to create temp dir: "+err.Error())
		return
	}

	files := r.MultipartForm.File["protos"]
	if len(files) == 0 {
		os.RemoveAll(tmpDir)
		writeErr(w, 400, "no files uploaded")
		return
	}

	var protoFiles []string
	var protosetFiles []string

	for _, fh := range files {
		src, err := fh.Open()
		if err != nil {
			os.RemoveAll(tmpDir)
			writeErr(w, 500, "failed to read uploaded file: "+err.Error())
			return
		}

		dstPath := filepath.Join(tmpDir, filepath.Base(fh.Filename))
		dst, err := os.Create(dstPath)
		if err != nil {
			src.Close()
			os.RemoveAll(tmpDir)
			writeErr(w, 500, "failed to write file: "+err.Error())
			return
		}

		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			os.RemoveAll(tmpDir)
			writeErr(w, 500, "failed to write file: "+err.Error())
			return
		}

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		switch ext {
		case ".proto":
			protoFiles = append(protoFiles, filepath.Base(fh.Filename))
		case ".protoset", ".pb", ".bin":
			protosetFiles = append(protosetFiles, dstPath)
		default:
			os.RemoveAll(tmpDir)
			writeErr(w, 400, fmt.Sprintf("unsupported file type %q (expected .proto, .protoset, .pb, or .bin)", ext))
			return
		}
	}

	var source grpcurl.DescriptorSource

	if len(protosetFiles) > 0 {
		// Use protoset files (pre-compiled descriptor sets).
		source, err = grpcurl.DescriptorSourceFromProtoSets(protosetFiles...)
		if err != nil {
			os.RemoveAll(tmpDir)
			writeErr(w, 400, "failed to parse protoset: "+err.Error())
			return
		}
	} else if len(protoFiles) > 0 {
		// Parse .proto source files with the temp dir as the import path.
		source, err = grpcurl.DescriptorSourceFromProtoFiles([]string{tmpDir}, protoFiles...)
		if err != nil {
			os.RemoveAll(tmpDir)
			writeErr(w, 400, "failed to parse proto files: "+err.Error())
			return
		}
	}

	if !store.updateSource(target, source, tmpDir) {
		os.RemoveAll(tmpDir)
		writeErr(w, 400, "connection was lost")
		return
	}

	svcs, err := grpcurl.ListServices(source)
	if err != nil {
		writeErr(w, 500, "failed to list services: "+err.Error())
		return
	}

	writeJSON(w, 200, map[string]interface{}{
		"services": svcs,
	})
}
