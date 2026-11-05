package server

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"nextboard/internal/vault"
)

// Credential vault (docs/API-vault.md). Everything that passes through here is ciphertext: the
// browser encrypts with a key from a master password the hub never receives. Creating, rekeying
// and resetting the vault can lock the owner out, so a paired device's token cannot do them.

func (s *Server) setupVault() {
	s.credVault = vault.New(s.st)
	s.credVault.Publish = s.events.Publish
}

func vaultErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vault.ErrAbsent):
		writeErrCode(w, http.StatusNotFound, "vault_absent", err.Error())
	case errors.Is(err, vault.ErrExists):
		writeErrCode(w, http.StatusConflict, "vault_exists", err.Error())
	case errors.Is(err, vault.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "item_not_found", err.Error())
	case errors.Is(err, vault.ErrConflict):
		writeErrCode(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, vault.ErrPinGone):
		writeErrCode(w, http.StatusGone, "pin_gone", err.Error())
	case errors.Is(err, vault.ErrFull):
		writeErrCode(w, http.StatusRequestEntityTooLarge, "vault_full", err.Error())
	case errors.Is(err, vault.ErrUnreadable):
		writeErrCode(w, http.StatusInternalServerError, "vault_unreadable", err.Error())
	case errors.Is(err, vault.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		log.Printf("vault: %v", err)
		writeErr(w, http.StatusInternalServerError, "vault gagal disimpan")
	}
}

func (s *Server) handleVaultGet(w http.ResponseWriter, r *http.Request, p *principal) {
	v, err := s.credVault.Get()
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleVaultInit(w http.ResponseWriter, r *http.Request, p *principal) {
	var k vault.KeyMaterial
	if !decode(w, r, &k) {
		return
	}
	h, err := s.credVault.Init(k)
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleVaultKey(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		vault.KeyMaterial
		BaseKeyRev int64 `json:"baseKeyRev"`
	}
	if !decode(w, r, &req) {
		return
	}
	h, err := s.credVault.SetKey(req.KeyMaterial, req.BaseKeyRev)
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleVaultPut(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Blob    string `json:"blob"`
		BaseRev int64  `json:"baseRev"`
	}
	if !decode(w, r, &req) {
		return
	}
	it, err := s.credVault.Put(r.PathValue("id"), req.Blob, req.BaseRev)
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) handleVaultDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	base, err := strconv.ParseInt(r.URL.Query().Get("baseRev"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "baseRev wajib ada")
		return
	}
	if err := s.credVault.Delete(r.PathValue("id"), base); err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleVaultBatch(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Items []vault.NewItem `json:"items"`
	}
	if !decode(w, r, &req) {
		return
	}
	out, err := s.credVault.Batch(req.Items)
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleVaultPinCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Proof string `json:"proof"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, secret, err := s.credVault.CreatePin(req.Proof)
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "secret": secret})
}

func (s *Server) handleVaultPinOpen(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Proof string `json:"proof"`
	}
	if !decode(w, r, &req) {
		return
	}
	secret, left, err := s.credVault.OpenPin(r.PathValue("id"), req.Proof)
	if errors.Is(err, vault.ErrWrongPin) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": err.Error(), "code": "wrong_pin", "left": left})
		return
	}
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"secret": secret})
}

func (s *Server) handleVaultPinDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.credVault.DeletePin(r.PathValue("id")); err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleVaultPinsClear(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.credVault.ClearPins(); err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleVaultReset(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Confirm != "RESET" {
		writeErr(w, http.StatusBadRequest, `konfirmasi harus "RESET"`)
		return
	}
	kept, err := s.credVault.Reset()
	if err != nil {
		vaultErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kept": kept})
}
