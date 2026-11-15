package auth

import "github.com/azamrizki/nextboard/internal/store"

// SetUpIn reports whether the hub whose data folder st is has an admin password: another
// instance of this machine, read without opening it (POST /api/instances/import).
func SetUpIn(st *store.Store) bool {
	var f authFile
	ok, err := st.Load(authFileName, &f)
	return err == nil && ok && f.PasswordHash != ""
}

// CheckPasswordIn reports whether password is the admin password of the hub whose data folder
// st is. Nothing in that folder is written: no session, no reload of a running hub's state.
func CheckPasswordIn(st *store.Store, password string) (bool, error) {
	var f authFile
	if _, err := st.Load(authFileName, &f); err != nil {
		return false, err
	}
	if f.PasswordHash == "" {
		return false, ErrNotSetup
	}
	return VerifyPassword(password, f.PasswordHash), nil
}
