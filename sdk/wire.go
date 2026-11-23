package sdk

// The answers of the API's own routes (not a module's), shared by the
// server and by its clients.

// Version is GET /v1/version: what runs, and how it was installed.
type Version struct {
	Version string `json:"version"`
	Dev     bool   `json:"dev"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	// Schema is the state database's version.
	Schema int `json:"schema"`
	// Install is empty for a hostd the installer did not set up.
	Install
}

// Install is what the installer's layout says of this hostd: the versions
// on disk, and the one before. Absent for a development build.
type Install struct {
	Previous       string   `json:"previous,omitempty"`
	RolledBackFrom string   `json:"rolled_back_from,omitempty"`
	Installed      []string `json:"installed,omitempty"`
}

// UpdateResult is the answer of POST /v1/update: the binary is installed
// and hostd restarts into it.
type UpdateResult struct {
	Version    string `json:"version"`
	Previous   string `json:"previous"`
	Restarting bool   `json:"restarting"`
}

// ReleaseStarted is the answer of POST /v1/update/release: the installer's
// update to the newest release was started.
type ReleaseStarted struct {
	Started bool `json:"started"`
}

// ReleaseRunning is the answer of GET /v1/update/release: whether that
// update runs now.
type ReleaseRunning struct {
	Running bool `json:"running"`
}
