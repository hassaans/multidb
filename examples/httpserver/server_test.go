package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hassaans/multidb"
	"github.com/hassaans/multidb/internal/pgtest"
)

func TestMain(m *testing.M) { pgtest.Main(m) }

type client struct {
	t     *testing.T
	base  string
	token string
}

// call sends a JSON request and returns the status and decoded body (nil for 204).
func (c client) call(method, path string, body any) (int, map[string]any) {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			c.t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, c.base+path, &buf)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode == http.StatusNoContent {
		return res.StatusCode, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		c.t.Fatalf("%s %s: non-JSON body %q", method, path, raw)
	}
	return res.StatusCode, out
}

func (c client) want(code int, method, path string, body any) map[string]any {
	c.t.Helper()
	got, out := c.call(method, path, body)
	if got != code {
		c.t.Fatalf("%s %s: want %d, got %d: %v", method, path, code, got, out)
	}
	return out
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	ctx := context.Background()
	store, err := multidb.Open(ctx, pgtest.AppDSN(t),
		multidb.WithSchema(pgtest.NewSchema(t)), multidb.WithAdminDSN(pgtest.AdminDSN(t)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if err := store.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(store, StaticTokens{"tok-a": "tenant-a", "tok-b": "tenant-b"}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEndToEnd(t *testing.T) {
	srv := newServer(t)
	a := client{t: t, base: srv.URL, token: "tok-a"}
	b := client{t: t, base: srv.URL, token: "tok-b"}
	anon := client{t: t, base: srv.URL}

	// Authentication.
	anon.want(http.StatusUnauthorized, "GET", "/v1/databases", nil)
	client{t: t, base: srv.URL, token: "wrong"}.want(http.StatusUnauthorized, "GET", "/v1/databases", nil)

	// Databases and collections.
	db := a.want(http.StatusCreated, "POST", "/v1/databases", map[string]any{"name": "shop"})
	dbID := db["id"].(string)
	a.want(http.StatusConflict, "POST", "/v1/databases", map[string]any{"name": "shop"})
	if list := a.want(http.StatusOK, "GET", "/v1/databases", nil); len(list["databases"].([]any)) != 1 {
		t.Fatalf("list databases: %v", list)
	}
	col := a.want(http.StatusCreated, "POST", "/v1/databases/"+dbID+"/collections", map[string]any{"name": "orders"})
	colID := col["id"].(string)
	a.want(http.StatusNotFound, "POST", "/v1/databases/00000000-0000-0000-0000-000000000000/collections", map[string]any{"name": "x"})

	// Fields.
	for _, f := range []map[string]any{{"name": "status", "type": "text"}, {"name": "amount", "type": "number"}} {
		a.want(http.StatusCreated, "POST", "/v1/collections/"+colID+"/fields", f)
	}
	a.want(http.StatusBadRequest, "POST", "/v1/collections/"+colID+"/fields", map[string]any{"name": "id", "type": "text"})
	fields := a.want(http.StatusOK, "GET", "/v1/collections/"+colID+"/fields", nil)
	if len(fields["fields"].([]any)) != 2 {
		t.Fatalf("fields: %v", fields)
	}

	// Records.
	ins := a.want(http.StatusCreated, "POST", "/v1/collections/"+colID+"/records", map[string]any{
		"docs": []any{map[string]any{"status": "open", "amount": 120}, map[string]any{"status": "closed", "amount": 80}},
	})
	ids := ins["ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("insert ids: %v", ins)
	}
	rid := ids[0].(string)
	a.want(http.StatusBadRequest, "POST", "/v1/collections/"+colID+"/records", map[string]any{"docs": []any{map[string]any{"amount": "NaN"}}})

	got := a.want(http.StatusOK, "GET", "/v1/collections/"+colID+"/records/"+rid, nil)
	if got["status"] != "open" || got["id"] != rid {
		t.Fatalf("get record: %v", got)
	}
	a.want(http.StatusNoContent, "PATCH", "/v1/collections/"+colID+"/records/"+rid, map[string]any{"status": "paid"})
	if got := a.want(http.StatusOK, "GET", "/v1/collections/"+colID+"/records/"+rid, nil); got["status"] != "paid" {
		t.Fatalf("patch not applied: %v", got)
	}

	// Query.
	res := a.want(http.StatusOK, "POST", "/v1/query", map[string]any{
		"collection": colID, "filter": map[string]any{"amount": map[string]any{"$gte": 100}},
	})
	if rows := res["rows"].([]any); len(rows) != 1 {
		t.Fatalf("query: %v", res)
	}
	a.want(http.StatusBadRequest, "POST", "/v1/query", map[string]any{"collection": colID, "filter": map[string]any{"nope": 1}})
	a.want(http.StatusUnprocessableEntity, "POST", "/v1/query", map[string]any{"collection": colID, "limit": 999999})
	a.want(http.StatusBadRequest, "POST", "/v1/query", bytes.NewBufferString("{not json"))

	// Isolation: tenant b sees none of it.
	b.want(http.StatusNotFound, "POST", "/v1/query", map[string]any{"collection": colID})
	b.want(http.StatusNotFound, "GET", "/v1/collections/"+colID+"/records/"+rid, nil)
	b.want(http.StatusNotFound, "POST", "/v1/collections/"+colID+"/records", map[string]any{"docs": []any{map[string]any{"status": "x"}}})
	if list := b.want(http.StatusOK, "GET", "/v1/databases", nil); len(list["databases"].([]any)) != 0 {
		t.Fatalf("tenant b databases: %v", list)
	}

	// Delete.
	del := a.want(http.StatusOK, "DELETE", "/v1/collections/"+colID+"/records", map[string]any{"ids": []any{rid, "00000000-0000-0000-0000-000000000000"}})
	if del["deleted"] != 1.0 {
		t.Fatalf("delete: %v", del)
	}
	a.want(http.StatusNoContent, "DELETE", "/v1/collections/"+colID, nil)
	a.want(http.StatusNotFound, "DELETE", "/v1/collections/"+colID, nil)
	a.want(http.StatusNoContent, "DELETE", "/v1/databases/"+dbID, nil)

	// Malformed ids are 400, unknown routes 404.
	a.want(http.StatusBadRequest, "GET", "/v1/collections/not-a-uuid/fields", nil)
	a.want(http.StatusNotFound, "GET", "/v1/nothing", nil)
}

func TestStaticTokensAuthenticator(t *testing.T) {
	auth := StaticTokens{"abc": "tenant-1"}
	for _, h := range []string{"", "Basic abc", "Bearer ", "Bearer nope"} {
		req := httptest.NewRequest("GET", "/", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		if _, err := auth.Tenant(req); err == nil {
			t.Errorf("header %q should be rejected", h)
		}
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer abc")
	if tenant, err := auth.Tenant(req); err != nil || tenant != "tenant-1" {
		t.Fatalf("want tenant-1, got %q %v", tenant, err)
	}

	parsed, err := ParseStaticTokens("abc=tenant-1, def=tenant-2")
	if err != nil || parsed["abc"] != "tenant-1" || parsed["def"] != "tenant-2" {
		t.Fatalf("ParseStaticTokens: %v %v", parsed, err)
	}
	for _, bad := range []string{"abc", "=tenant", "abc=", "abc=tenant-1,abc=tenant-2"} {
		if _, err := ParseStaticTokens(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestStatusFor(t *testing.T) {
	cases := map[error]int{
		multidb.ErrNotFound:        http.StatusNotFound,
		multidb.ErrUnknownField:    http.StatusBadRequest,
		multidb.ErrUnknownOperator: http.StatusBadRequest,
		multidb.ErrTypeMismatch:    http.StatusBadRequest,
		multidb.ErrBadSpec:         http.StatusBadRequest,
		multidb.ErrLimitExceeded:   http.StatusUnprocessableEntity,
		multidb.ErrConflict:        http.StatusConflict,
		multidb.ErrForbidden:       http.StatusForbidden,
		multidb.ErrQuota:           http.StatusPaymentRequired,
		fmt.Errorf("boom"):         http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got := statusFor(&multidb.Error{Kind: err, Message: "x"}); got != want {
			t.Errorf("%v: want %d, got %d", err, want, got)
		}
	}
}
