package update

import (
	"path/filepath"
	"strings"
)

// Method is how audd was installed, which decides how it is updated.
type Method struct {
	Name string `json:"method"` // homebrew, npm, npx, pip, pipx, uv, uvx, scoop, winget, go, docker, binary
	// Command updates this install; empty for SelfUpdate installs.
	Command string `json:"command,omitempty"`
	// SelfUpdate is true when audd replaces its own binary.
	SelfUpdate bool `json:"self_update"`
}

// Commands for installs a package manager owns.
const (
	BrewCommand   = "brew upgrade audd"
	NPMCommand    = "npm install -g @audd/cli@latest"
	NPXCommand    = "npx @audd/cli@latest"
	PipCommand    = "pip install --upgrade audd-cli"
	PipxCommand   = "pipx upgrade audd-cli"
	UVCommand     = "uv tool upgrade audd-cli"
	UVXCommand    = "uvx audd-cli@latest"
	ScoopCommand  = "scoop update audd"
	WingetCommand = "winget upgrade --id AudD.CLI"
	GoCommand     = "go install github.com/AudDMusic/audd-cli/cmd/audd@latest"
	DockerCommand = "docker pull ghcr.io/auddmusic/audd-cli:latest"
)

// methods by name, for AUDD_INSTALL_METHOD.
var methods = map[string]Method{
	"homebrew": {Name: "homebrew", Command: BrewCommand},
	"npm":      {Name: "npm", Command: NPMCommand},
	"npx":      {Name: "npx", Command: NPXCommand},
	"pip":      {Name: "pip", Command: PipCommand},
	"pipx":     {Name: "pipx", Command: PipxCommand},
	"uv":       {Name: "uv", Command: UVCommand},
	"uvx":      {Name: "uvx", Command: UVXCommand},
	"scoop":    {Name: "scoop", Command: ScoopCommand},
	"winget":   {Name: "winget", Command: WingetCommand},
	"go":       {Name: "go", Command: GoCommand},
	"docker":   {Name: "docker", Command: DockerCommand},
}

// DetectMethod works out how the binary at exe was installed. A package
// can say so with AUDD_INSTALL_METHOD (the Docker image sets "docker");
// otherwise the path decides. getenv reads AUDD_INSTALL_METHOD, GOPATH,
// and GOBIN.
func DetectMethod(exe, goos string, getenv func(string) string) Method {
	if getenv != nil {
		if m, ok := methods[strings.ToLower(strings.TrimSpace(getenv("AUDD_INSTALL_METHOD")))]; ok {
			return m
		}
	}
	p := strings.ToLower(filepath.ToSlash(exe))
	if goos == "windows" {
		p = strings.ReplaceAll(strings.ToLower(exe), `\`, "/")
	}
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(p, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("/cellar/", "/caskroom/", "/homebrew/", "/linuxbrew/"):
		return methods["homebrew"]
	// npx and uvx run audd from their caches; the generic npm and pip
	// paths below would match those too.
	case has("/_npx/"):
		return methods["npx"]
	case has("/uv/archive-v", "/uv/cache/archive-v"):
		return methods["uvx"]
	case has("/node_modules/"):
		return methods["npm"]
	case has("/pipx/"):
		return methods["pipx"]
	case has("/uv/tools/"):
		return methods["uv"]
	case has("/site-packages/", "/dist-packages/"):
		return methods["pip"]
	case has("/scoop/"):
		return methods["scoop"]
	case has("/winget/"):
		return methods["winget"]
	}
	if isGoBin(p, goos, getenv) {
		return methods["go"]
	}
	return Method{Name: "binary", SelfUpdate: true}
}

func isGoBin(p, goos string, getenv func(string) string) bool {
	dir := p[:max(strings.LastIndex(p, "/"), 0)]
	norm := func(s string) string {
		s = filepath.ToSlash(s)
		if goos == "windows" {
			s = strings.ReplaceAll(s, `\`, "/")
		}
		return strings.TrimRight(strings.ToLower(s), "/")
	}
	if getenv != nil {
		if b := getenv("GOBIN"); b != "" && norm(b) == dir {
			return true
		}
		if gp := getenv("GOPATH"); gp != "" {
			sep := ":"
			if goos == "windows" {
				sep = ";"
			}
			for _, d := range strings.Split(gp, sep) {
				if d != "" && norm(d)+"/bin" == dir {
					return true
				}
			}
		}
	}
	// The default GOPATH is ~/go.
	return strings.HasSuffix(dir, "/go/bin")
}
