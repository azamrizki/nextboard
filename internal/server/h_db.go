package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/azamrizki/nextboard/internal/dbx"
	"github.com/azamrizki/nextboard/internal/project"
)

// The database browser (docs/API-db.md): connections of a project, their schema, rows, SQL and
// Redis keys. The hub does the connecting; the browser never reaches a database.

func (s *Server) routesDB(authed func(string, handler)) {
	authed("GET /api/projects/{id}/dbs", s.handleDBList)
	authed("POST /api/projects/{id}/dbs", s.handleDBCreate)
	authed("GET /api/projects/{id}/dbs/detect", s.handleDBDetect)
	authed("GET /api/projects/{id}/db-queries", s.handleDBQueries)
	authed("POST /api/projects/{id}/db-queries", s.handleDBQuerySave)
	authed("DELETE /api/projects/{id}/db-queries/{qid}", s.handleDBQueryDelete)
	authed("POST /api/dbs/test", s.handleDBTest)
	authed("GET /api/dbs/{cid}", s.handleDBGet)
	authed("PUT /api/dbs/{cid}", s.handleDBReplace)
	authed("DELETE /api/dbs/{cid}", s.handleDBDelete)
	authed("POST /api/dbs/{cid}/trust", s.handleDBTrust)
	authed("POST /api/dbs/{cid}/password", s.handleDBPassword)
	authed("POST /api/dbs/{cid}/disconnect", s.handleDBDisconnect)
	authed("GET /api/dbs/{cid}/schema", s.handleDBSchema)
	authed("GET /api/dbs/{cid}/table", s.handleDBTable)
	authed("POST /api/dbs/{cid}/rows", s.handleDBRows)
	authed("POST /api/dbs/{cid}/run", s.handleDBRun)
	authed("POST /api/dbs/{cid}/sessions/{sid}/finish", s.handleDBFinish)
	authed("POST /api/dbs/{cid}/sessions/{sid}/cancel", s.handleDBCancel)
	authed("POST /api/dbs/{cid}/apply", s.handleDBApply)
	authed("POST /api/dbs/{cid}/export", s.handleDBExport)
	authed("GET /api/dbs/{cid}/history", s.handleDBHistory)
	authed("GET /api/dbs/{cid}/activity", s.handleDBActivity)
	authed("GET /api/dbs/{cid}/keys", s.handleDBKeys)
	authed("GET /api/dbs/{cid}/key", s.handleDBKey)
	authed("POST /api/dbs/{cid}/key", s.handleDBKeyWrite)
	authed("POST /api/dbs/{cid}/command", s.handleDBCommand)
	authed("GET /api/db-requests", s.handleDBRequests)
	authed("POST /api/db-requests/{id}/approve", s.handleDBRequestApprove)
	authed("POST /api/db-requests/{id}/deny", s.handleDBRequestDeny)
	authed("POST /api/dbs/{cid}/ai/filters", s.handleDBAIFilters)
	authed("POST /api/dbs/{cid}/ai/sql", s.handleDBAISQL)
	authed("POST /api/dbs/{cid}/ai/chat", s.handleDBAIChat)
}

// dbError writes what went wrong in the shape the web reads: confirmations as 409 with what to
// confirm, read-only as 403, a driver's error as 422 with what to do about it.
func dbError(w http.ResponseWriter, err error) {
	var ce *dbx.ConfirmError
	var ro *dbx.ReadOnlyError
	var hk *dbx.HostKeyError
	switch {
	case errors.As(err, &ce):
		writeJSON(w, http.StatusConflict, map[string]any{"error": ce.Error(), "code": ce.Code, "statements": ce.Stmts, "rows": ce.Rows})
	case errors.As(err, &ro):
		writeErrCode(w, http.StatusForbidden, "read_only", ro.Error())
	case errors.As(err, &hk):
		writeJSON(w, http.StatusConflict, map[string]any{"error": hk.Error(), "code": "host_key", "hostKey": hk})
	case errors.Is(err, dbx.ErrPasswordNeeded):
		writeErrCode(w, http.StatusConflict, "password_needed", "this connection does not save its password: give it to connect")
	case errors.Is(err, dbx.ErrPending):
		writeErrCode(w, http.StatusConflict, "pending", err.Error())
	case errors.Is(err, dbx.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, dbx.ErrInvalid):
		writeErrCode(w, http.StatusBadRequest, "invalid", strings.TrimPrefix(err.Error(), "invalid: "))
	case errors.Is(err, context.Canceled):
		writeErrCode(w, http.StatusConflict, "stopped", "stopped")
	default:
		writeErrCode(w, http.StatusUnprocessableEntity, "db_error", dbx.Friendly(err))
	}
}

// dbProject checks the project of a request.
func (s *Server) dbProject(w http.ResponseWriter, r *http.Request) (project.Project, bool) {
	pr, ok := s.projects.Get(r.PathValue("id"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", "project not found")
		return project.Project{}, false
	}
	return pr, true
}

// dbConn checks the connection of a request.
func (s *Server) dbConn(w http.ResponseWriter, r *http.Request) (dbx.Connection, bool) {
	c, ok := s.dbs.Store.Get(r.PathValue("cid"))
	if !ok {
		writeErrCode(w, http.StatusNotFound, "not_found", "no such connection")
		return dbx.Connection{}, false
	}
	return c, true
}

func (s *Server) dbPublish(c dbx.Connection) {
	s.events.Publish(map[string]any{"type": "db", "connection": c, "status": s.dbs.StatusOf(c.ID)})
}

func (s *Server) handleDBList(w http.ResponseWriter, r *http.Request, _ *principal) {
	pr, ok := s.dbProject(w, r)
	if !ok {
		return
	}
	list := s.dbs.Store.List(pr.ID)
	status := map[string]dbx.Status{}
	for _, c := range list {
		status[c.ID] = s.dbs.StatusOf(c.ID)
	}
	if list == nil {
		list = []dbx.Connection{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": list, "status": status})
}

type dbBody struct {
	Connection dbx.Connection `json:"connection"`
	Secrets    dbx.Secrets    `json:"secrets"`
	// DetectRef: the password is the one of a file Detect found, read again here
	DetectRef string `json:"detectRef,omitempty"`
	// ID: Test of an existing connection's edited settings uses its kept secrets
	ID string `json:"id,omitempty"`
}

func (s *Server) handleDBCreate(w http.ResponseWriter, r *http.Request, _ *principal) {
	pr, ok := s.dbProject(w, r)
	if !ok {
		return
	}
	var b dbBody
	if !decode(w, r, &b) {
		return
	}
	if err := s.dbCheckMachine(pr, b.Connection); err != nil {
		dbError(w, err)
		return
	}
	if b.DetectRef != "" && b.Secrets.Password == nil {
		pw, err := dbx.DetectedPassword(r.Context(), s.machines.Host, b.DetectRef)
		if err != nil {
			dbError(w, err)
			return
		}
		b.Secrets.Password = &pw
	}
	b.Connection.ProjectID = pr.ID
	c, err := s.dbs.Store.Add(b.Connection, b.Secrets)
	if err != nil {
		dbError(w, err)
		return
	}
	s.dbPublish(c)
	writeJSON(w, http.StatusCreated, c)
}

// dbCheckMachine: a connection reached through a machine names one of the hub's.
func (s *Server) dbCheckMachine(_ project.Project, c dbx.Connection) error {
	if c.Reach.Kind == dbx.ReachMachine {
		if _, ok := s.machines.Host(c.Reach.MachineID); !ok {
			return fmt.Errorf("%w: no machine %s", dbx.ErrInvalid, c.Reach.MachineID)
		}
	}
	return nil
}

func (s *Server) handleDBGet(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connection": c, "status": s.dbs.StatusOf(c.ID)})
}

func (s *Server) handleDBReplace(w http.ResponseWriter, r *http.Request, _ *principal) {
	old, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var b dbBody
	if !decode(w, r, &b) {
		return
	}
	pr, _ := s.projects.Get(old.ProjectID)
	if err := s.dbCheckMachine(pr, b.Connection); err != nil {
		dbError(w, err)
		return
	}
	c, err := s.dbs.Store.Replace(old.ID, b.Connection, b.Secrets)
	if err != nil {
		dbError(w, err)
		return
	}
	s.dbs.Drop2(c.ID)
	s.dbPublish(c)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDBDelete(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	s.dbs.Drop(c.ID)
	if err := s.dbs.Store.Delete(c.ID); err != nil {
		dbError(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "dbRemoved", "id": c.ID, "projectId": c.ProjectID})
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDBTest(w http.ResponseWriter, r *http.Request, _ *principal) {
	var b dbBody
	if !decode(w, r, &b) {
		return
	}
	if b.ID != "" {
		if old, ok := s.dbs.Store.Get(b.ID); ok {
			b.Connection.ProjectID = old.ProjectID
		} else {
			b.ID = ""
		}
	}
	if b.DetectRef != "" && b.Secrets.Password == nil {
		if pw, err := dbx.DetectedPassword(r.Context(), s.machines.Host, b.DetectRef); err == nil {
			b.Secrets.Password = &pw
		}
	}
	res, err := s.dbs.Test(r.Context(), b.Connection, b.Secrets, b.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDBTrust(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var b struct {
		Hop         int    `json:"hop"`
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !strings.HasPrefix(b.Fingerprint, "SHA256:") {
		writeErrCode(w, http.StatusBadRequest, "invalid", "fingerprint: SHA256:…")
		return
	}
	c, err := s.dbs.Store.TrustHostKey(c.ID, b.Hop, b.Fingerprint)
	if err != nil {
		dbError(w, err)
		return
	}
	s.dbs.Drop2(c.ID)
	s.dbPublish(c)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDBPassword(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var b struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &b) {
		return
	}
	s.dbs.SetPassword(c.ID, b.Password)
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDBDisconnect(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	s.dbs.Drop2(c.ID)
	s.dbPublish(c)
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDBDetect(w http.ResponseWriter, r *http.Request, _ *principal) {
	pr, ok := s.dbProject(w, r)
	if !ok {
		return
	}
	parts := pr.Parts
	if len(parts) == 0 {
		parts = []project.Part{{MachineID: pr.MachineID, Path: pr.Path}}
	}
	var folders []dbx.Folder
	for _, pt := range parts {
		if h, ok := s.machines.Host(pt.MachineID); ok && h.Connected() {
			folders = append(folders, dbx.Folder{Part: pt.Name, MachineID: pt.MachineID, Host: h, Path: pt.Path, Local: pt.MachineID == "local"})
		}
	}
	list := dbx.Detect(r.Context(), folders)
	if list == nil {
		list = []dbx.Candidate{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": list})
}

func (s *Server) handleDBSchema(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	if c.Driver.IsKV() {
		e, err := s.dbs.KV(r.Context(), c.ID)
		if err != nil {
			dbError(w, err)
			return
		}
		info, err := e.Info(r.Context())
		if err != nil {
			dbError(w, err)
			return
		}
		s.dbPublishStatusOnce(c)
		writeJSON(w, http.StatusOK, map[string]any{"kv": info})
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	sc, err := e.Schema(r.Context(), r.URL.Query().Get("current"))
	if err != nil {
		dbError(w, err)
		return
	}
	s.dbPublishStatusOnce(c)
	writeJSON(w, http.StatusOK, map[string]any{"schema": sc})
}

// dbPublishStatusOnce tells the pages a connection is open now (its status chip).
func (s *Server) dbPublishStatusOnce(c dbx.Connection) {
	s.events.Publish(map[string]any{"type": "dbStatus", "id": c.ID, "status": s.dbs.StatusOf(c.ID)})
}

func (s *Server) handleDBTable(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	q := r.URL.Query()
	t, err := e.Table(r.Context(), q.Get("schema"), q.Get("name"))
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDBRows(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var rq dbx.RowsRequest
	if !decode(w, r, &rq) {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	page, err := e.Rows(r.Context(), rq)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleDBRun(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var o dbx.RunOpts
	if !decode(w, r, &o) {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	res, err := e.Run(r.Context(), o)
	if err != nil {
		dbError(w, err)
		return
	}
	for _, x := range res {
		if x.Kind == dbx.KindDDL && x.Error == "" {
			e.Forget()
			s.events.Publish(map[string]any{"type": "dbSchema", "id": c.ID})
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

func (s *Server) handleDBFinish(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var b struct {
		Commit bool `json:"commit"`
	}
	if !decode(w, r, &b) {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	if err := e.Finish(r.PathValue("sid"), b.Commit); err != nil {
		dbError(w, err)
		return
	}
	e.Forget()
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleDBCancel(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stopped": e.Cancel(r.PathValue("sid"))})
}

func (s *Server) handleDBApply(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var rq dbx.ApplyRequest
	if !decode(w, r, &rq) {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	res, err := e.Apply(r.Context(), rq)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDBExport(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var rq dbx.ExportRequest
	if !decode(w, r, &rq) {
		return
	}
	e, err := s.dbs.SQL(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	name := "export"
	if rq.Rows != nil {
		name = rq.Rows.Table
	}
	ctype := map[string]string{"csv": "text/csv; charset=utf-8", "json": "application/json", "sql": "application/sql"}[rq.Format]
	if ctype == "" {
		writeErrCode(w, http.StatusBadRequest, "invalid", "format: csv, json or sql")
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", safeName(name)+"."+rq.Format))
	w.Header().Set("Cache-Control", "no-store")
	if err := e.Export(r.Context(), rq, w); err != nil {
		// headers are out; the error ends the file where the reader sees it
		fmt.Fprintf(w, "\n-- export stopped: %s\n", err)
	}
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "export"
	}
	return b.String()
}

func (s *Server) handleDBHistory(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 500 {
		n = 100
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.dbs.Store.History(c.ID, n)})
}

func (s *Server) handleDBActivity(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if n <= 0 || n > 2000 {
		n = 100
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.dbs.Store.ActivityLog(c.ID, n)})
}

func (s *Server) handleDBKeys(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	e, err := s.dbs.KV(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	q := r.URL.Query()
	db, _ := strconv.Atoi(q.Get("db"))
	cur, _ := strconv.ParseUint(q.Get("cursor"), 10, 64)
	count, _ := strconv.ParseInt(q.Get("count"), 10, 64)
	page, err := e.Scan(r.Context(), db, cur, q.Get("match"), q.Get("type"), count)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleDBKey(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	e, err := s.dbs.KV(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	q := r.URL.Query()
	db, _ := strconv.Atoi(q.Get("db"))
	kv, err := e.Key(r.Context(), db, q.Get("name"), q.Get("page"))
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, kv)
}

func (s *Server) handleDBKeyWrite(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var kw dbx.KeyWrite
	if !decode(w, r, &kw) {
		return
	}
	e, err := s.dbs.KV(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	out, err := e.Write(r.Context(), kw)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": out})
}

func (s *Server) handleDBCommand(w http.ResponseWriter, r *http.Request, _ *principal) {
	c, ok := s.dbConn(w, r)
	if !ok {
		return
	}
	var o dbx.CommandOpts
	if !decode(w, r, &o) {
		return
	}
	e, err := s.dbs.KV(r.Context(), c.ID)
	if err != nil {
		dbError(w, err)
		return
	}
	out, err := e.Command(r.Context(), o)
	if err != nil {
		dbError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"output": out})
}

func (s *Server) handleDBQueries(w http.ResponseWriter, r *http.Request, _ *principal) {
	pr, ok := s.dbProject(w, r)
	if !ok {
		return
	}
	list := s.dbs.Store.Queries(pr.ID)
	if list == nil {
		list = []dbx.Query{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleDBQuerySave(w http.ResponseWriter, r *http.Request, _ *principal) {
	pr, ok := s.dbProject(w, r)
	if !ok {
		return
	}
	var q dbx.Query
	if !decode(w, r, &q) {
		return
	}
	q.ProjectID = pr.ID
	q, err := s.dbs.Store.SaveQuery(q)
	if err != nil {
		dbError(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "dbQueries", "projectId": pr.ID})
	writeJSON(w, http.StatusOK, q)
}

func (s *Server) handleDBQueryDelete(w http.ResponseWriter, r *http.Request, _ *principal) {
	pr, ok := s.dbProject(w, r)
	if !ok {
		return
	}
	if err := s.dbs.Store.DeleteQuery(pr.ID, r.PathValue("qid")); err != nil {
		dbError(w, err)
		return
	}
	s.events.Publish(map[string]any{"type": "dbQueries", "projectId": pr.ID})
	writeJSON(w, http.StatusOK, okBody)
}

// dbRemoveProject forgets the connections of a project deleted for good.
func (s *Server) dbRemoveProject(id string) {
	for _, cid := range s.dbs.Store.DeleteProject(id) {
		s.dbs.Drop(cid)
	}
}
