package apps

// Config is the apps module's section of hostd.toml:
//
//	[apps]
//	# The browser for web apps ({url} and {profile} are replaced), and the
//	# gamescope command for apps with window.wrap = "gamescope".
//	browser = ["chromium", "--kiosk", "--user-data-dir={profile}", "{url}"]
//	gamescope = ["gamescope", "-W", "1920", "-H", "1080", "-f"]
type Config struct {
	Browser   []string `toml:"browser"`
	Gamescope []string `toml:"gamescope"`
}
