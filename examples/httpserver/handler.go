package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/google/uuid"

	"github.com/hassaans/multidb"
)

type handler struct {
	store *multidb.Store
	auth  Authenticator
}

type ctxKey struct{}

// NewHandler mounts the API on a new ServeMux. Every route runs as the tenant the
// Authenticator resolved; unauthenticated requests get 401 before any route runs.
func NewHandler(store *multidb.Store, auth Authenticator) http.Handler {
	h := &handler{store: store, auth: auth}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/databases", h.listDatabases)
	mux.HandleFunc("POST /v1/databases", h.createDatabase)
	mux.HandleFunc("DELETE /v1/databases/{id}", h.deleteDatabase)
	mux.HandleFunc("GET /v1/databases/{id}/collections", h.listCollections)
	mux.HandleFunc("POST /v1/databases/{id}/collections", h.createCollection)
	mux.HandleFunc("PATCH /v1/collections/{id}", h.renameCollection)
	mux.HandleFunc("DELETE /v1/collections/{id}", h.deleteCollection)
	mux.HandleFunc("GET /v1/collections/{id}/fields", h.listFields)
	mux.HandleFunc("POST /v1/collections/{id}/fields", h.addField)
	mux.HandleFunc("PATCH /v1/fields/{id}", h.updateField)
	mux.HandleFunc("DELETE /v1/fields/{id}", h.deleteField)
	mux.HandleFunc("POST /v1/collections/{id}/records", h.insert)
	mux.HandleFunc("DELETE /v1/collections/{id}/records", h.deleteRecords)
	mux.HandleFunc("GET /v1/collections/{id}/records/{rid}", h.get)
	mux.HandleFunc("PATCH /v1/collections/{id}/records/{rid}", h.update)
	mux.HandleFunc("POST /v1/query", h.query)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeErr(w, http.StatusNotFound, errors.New("no such route"))
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenant, err := h.auth.Tenant(r)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, err)
			return
		}
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, tenant)))
	})
}

// tenant returns the handle for the authenticated tenant.
func (h *handler) tenant(r *http.Request) *multidb.Tenant {
	id, _ := r.Context().Value(ctxKey{}).(string)
	return h.store.Tenant(id)
}

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

func (h *handler) listDatabases(w http.ResponseWriter, r *http.Request) {
	dbs, err := h.tenant(r).ListDatabases(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if dbs == nil {
		dbs = []multidb.Database{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": dbs})
}

func (h *handler) createDatabase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	db, err := h.tenant(r).CreateDatabase(r.Context(), req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, db)
}

func (h *handler) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.tenant(r).DeleteDatabase(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Collections
// ---------------------------------------------------------------------------

func (h *handler) listCollections(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	cols, err := h.tenant(r).ListCollections(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if cols == nil {
		cols = []multidb.Collection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"collections": cols})
}

func (h *handler) createCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	col, err := h.tenant(r).CreateCollection(r.Context(), id, req.Name)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, col)
}

func (h *handler) renameCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := h.tenant(r).RenameCollection(r.Context(), id, req.Name); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.tenant(r).DeleteCollection(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Fields
// ---------------------------------------------------------------------------

func (h *handler) listFields(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	fields, err := h.tenant(r).ListFields(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if fields == nil {
		fields = []multidb.Field{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"fields": fields})
}

func (h *handler) addField(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var spec multidb.FieldSpec
	if !decode(w, r, &spec) {
		return
	}
	f, err := h.tenant(r).AddField(r.Context(), id, spec)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, f)
}

func (h *handler) updateField(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var spec multidb.FieldSpec
	if !decode(w, r, &spec) {
		return
	}
	if err := h.tenant(r).UpdateField(r.Context(), id, spec); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) deleteField(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := h.tenant(r).DeleteField(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Records and queries
// ---------------------------------------------------------------------------

func (h *handler) insert(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		Docs []json.RawMessage `json:"docs"`
	}
	if !decode(w, r, &req) {
		return
	}
	ids, err := h.tenant(r).Insert(r.Context(), id, req.Docs)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ids": ids})
}

func (h *handler) deleteRecords(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	n, err := h.tenant(r).Delete(r.Context(), id, req.IDs)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	rid, ok := pathID(w, r, "rid")
	if !ok {
		return
	}
	doc, err := h.tenant(r).Get(r.Context(), id, rid)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	rid, ok := pathID(w, r, "rid")
	if !ok {
		return
	}
	var patch json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	if err := h.tenant(r).Update(r.Context(), id, rid, patch); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) query(w http.ResponseWriter, r *http.Request) {
	var spec multidb.QuerySpec
	if !decode(w, r, &spec) {
		return
	}
	res, err := h.tenant(r).Query(r.Context(), spec)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

const maxBody = 4 << 20

// decode reads a JSON body into v, answering 400 on failure.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid JSON body"))
		return false
	}
	return true
}

// pathID parses a uuid path segment, answering 400 on failure.
func pathID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("invalid "+name))
		return uuid.Nil, false
	}
	return id, true
}

// statusFor maps the package's typed errors to HTTP status codes.
func statusFor(err error) int {
	switch {
	case errors.Is(err, multidb.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, multidb.ErrUnknownField), errors.Is(err, multidb.ErrUnknownOperator),
		errors.Is(err, multidb.ErrTypeMismatch), errors.Is(err, multidb.ErrBadSpec):
		return http.StatusBadRequest
	case errors.Is(err, multidb.ErrLimitExceeded):
		return http.StatusUnprocessableEntity
	case errors.Is(err, multidb.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, multidb.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, multidb.ErrQuota):
		return http.StatusPaymentRequired
	default:
		return http.StatusInternalServerError
	}
}

func fail(w http.ResponseWriter, err error) { writeErr(w, statusFor(err), err) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr answers with {"error": "..."}; 5xx details are logged, not leaked.
func writeErr(w http.ResponseWriter, code int, err error) {
	if code >= 500 {
		log.Printf("error: %v", err)
		err = errors.New("internal error")
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
