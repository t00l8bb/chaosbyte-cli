// Package config holds the per-team RoomConfig that the engine reads at
// startup. The same Go binary serves any number of teams; each team's room
// is the engine running against a different config.
//
// The flagship Vibespace instance loads DefaultVibespace. Other teams load
// their own config from a file (planned) or from a memory-resident map
// (current). The flagship is one customer of the platform, not the platform.
//
// New configuration surfaces get added by extending RoomConfig and giving
// every existing config a sensible default. The engine should always be
// able to run against an empty config that defaults to the flagship's
// behavior so adding a new field never breaks an existing team's room.
package config

import "github.com/charmbracelet/lipgloss"

// RoomConfig is the per-team configuration the platform reads to render a
// team's room. Each team has exactly one of these. The structure is grouped
// by surface so adding a field stays local.
type RoomConfig struct {
	// Slug is the URL-safe identifier the SSH server uses to route incoming
	// connections to this team's room. "vibespace" routes the flagship.
	// Empty falls back to "vibespace".
	Slug string

	Brand     BrandConfig
	Theme     ThemeConfig
	Mod       ModConfig
	Surfaces  SurfacesConfig
	Spotlight SpotlightConfig
}

// BrandConfig carries the team's visible identity. Name appears in the top
// bar of every room view. MOTD is the line the room introduces itself with
// when someone connects for the first time.
type BrandConfig struct {
	Name    string
	MOTD    string
	Tagline string
}

// ThemeConfig overrides the palette. Empty values fall back to the
// flagship's defaults so a team can override only the colors they want to.
type ThemeConfig struct {
	Bg       lipgloss.Color
	Fg       lipgloss.Color
	Muted    lipgloss.Color
	Accent   lipgloss.Color
	Accent2  lipgloss.Color
	BorderHi lipgloss.Color
	BorderLo lipgloss.Color
}

// ModConfig drives the team's moderator personality. The welcome line uses
// {nick} as a substitution token for the joining user. Prompt and idle
// thresholds set the moderator's cadence in seconds.
type ModConfig struct {
	Nick               string
	Welcome            string
	PromptIntervalSecs int
	IdleThresholdSecs  int
}

// SurfacesConfig toggles which content surfaces the team has enabled. The
// flagship enables Chat and Spotlight and Games. A research team might
// enable Chat plus Discussions but not Games. The engine renders only the
// enabled surfaces.
type SurfacesConfig struct {
	Chat        bool
	Spotlight   bool
	Games       bool
	Skills      bool // planned, off by default
	Discussions bool // planned, off by default

	// Dispatcher gates the Monobyte build surface: /run, /sh, /pull,
	// /scratch, /serve, /unserve, /agent. Vibespace ships with this
	// off — it's a chatroom product. Monobyte rooms (and any team
	// that opts in via their .toml) set it true to unlock the dev
	// verbs.
	Dispatcher bool
}

// SpotlightConfig names the currently spotlit project for this team. v1
// supports one static project per team; v2 will let the moderator rotate.
type SpotlightConfig struct {
	Name        string
	Author      string
	Description string
	RepoURL     string
}

// DefaultVibespace returns the flagship instance's configuration. Every
// new team config inherits these defaults for any field it leaves empty.
func DefaultVibespace() RoomConfig {
	return RoomConfig{
		Slug: "vibespace",
		Brand: BrandConfig{
			Name:    "vibespace",
			MOTD:    "the workshop is open. :help when you need it. :leave when you go.",
			Tagline: "a small room for those who are paying attention.",
		},
		// Flagship palette is the "boggy" theme registered in internal/theme.
		// Users can switch to "workshop" or any other registered theme via
		// /themes inside the room.
		Theme: ThemeConfig{
			Bg:       lipgloss.Color("#1a1b26"),
			Fg:       lipgloss.Color("#c0caf5"),
			Muted:    lipgloss.Color("#565f89"),
			Accent:   lipgloss.Color("#7aa2f7"),
			Accent2:  lipgloss.Color("#bb9af7"),
			BorderHi: lipgloss.Color("#7aa2f7"),
			BorderLo: lipgloss.Color("#3b4261"),
		},
		Mod: ModConfig{
			Nick:               "@mod",
			Welcome:            "welcome, {nick}. the workshop is yours for as long as you like.",
			PromptIntervalSecs: 90,
			IdleThresholdSecs:  45,
		},
		Surfaces: SurfacesConfig{
			Chat:      true,
			Spotlight: true,
			Games:     true,
		},
		Spotlight: SpotlightConfig{
			Name:        "tinytty",
			Author:      "rin",
			Description: "a 4kb terminal renderer",
			RepoURL:     "git.sr.ht/~rin/tinytty",
		},
	}
}

// DefaultMonobyte returns the built-in Monobyte team configuration.
// Same engine, same broker, same identity — but the dispatcher surface
// (/run, /sh, /pull, /scratch, /serve, /unserve, /agent) is unlocked.
// Users SSH `ssh monobyte@host` to land here.
//
// The strategic split: Vibespace is the chatroom product (community
// wedge). Monobyte is the build product (lights up the dev verbs).
// One server binary, two surfaces. Teams that want both rooms can
// run both slugs.
func DefaultMonobyte() RoomConfig {
	cfg := DefaultVibespace()
	cfg.Slug = "monobyte"
	cfg.Brand = BrandConfig{
		Name:    "monobyte",
		MOTD:    "the build room. /run, /pull, /agent — ship together.",
		Tagline: "where vibe coders and devs work the same canvas.",
	}
	cfg.Mod.Welcome = "welcome to monobyte, {nick}. /help for the verbs."
	cfg.Surfaces = SurfacesConfig{
		Chat:       true,
		Spotlight:  true,
		Games:      false,
		Dispatcher: true,
	}
	return cfg
}

// MergeWithDefaults takes a partial config a team has set and fills in any
// empty fields from the flagship defaults. This is what the loader returns:
// the union of the team's overrides and the engine's expected fields.
func MergeWithDefaults(team RoomConfig) RoomConfig {
	def := DefaultVibespace()
	if team.Slug == "" {
		team.Slug = def.Slug
	}
	team.Brand = mergeBrand(team.Brand, def.Brand)
	team.Theme = mergeTheme(team.Theme, def.Theme)
	team.Mod = mergeMod(team.Mod, def.Mod)
	team.Spotlight = mergeSpotlight(team.Spotlight, def.Spotlight)
	// Surfaces is a special case: a zero-valued struct means "all false"
	// rather than "use defaults." Teams must opt in explicitly. The flagship
	// path passes the defaults whole.
	return team
}

func mergeBrand(team, def BrandConfig) BrandConfig {
	if team.Name == "" {
		team.Name = def.Name
	}
	if team.MOTD == "" {
		team.MOTD = def.MOTD
	}
	if team.Tagline == "" {
		team.Tagline = def.Tagline
	}
	return team
}

func mergeTheme(team, def ThemeConfig) ThemeConfig {
	if team.Bg == "" {
		team.Bg = def.Bg
	}
	if team.Fg == "" {
		team.Fg = def.Fg
	}
	if team.Muted == "" {
		team.Muted = def.Muted
	}
	if team.Accent == "" {
		team.Accent = def.Accent
	}
	if team.Accent2 == "" {
		team.Accent2 = def.Accent2
	}
	if team.BorderHi == "" {
		team.BorderHi = def.BorderHi
	}
	if team.BorderLo == "" {
		team.BorderLo = def.BorderLo
	}
	return team
}

func mergeMod(team, def ModConfig) ModConfig {
	if team.Nick == "" {
		team.Nick = def.Nick
	}
	if team.Welcome == "" {
		team.Welcome = def.Welcome
	}
	if team.PromptIntervalSecs == 0 {
		team.PromptIntervalSecs = def.PromptIntervalSecs
	}
	if team.IdleThresholdSecs == 0 {
		team.IdleThresholdSecs = def.IdleThresholdSecs
	}
	return team
}

func mergeSpotlight(team, def SpotlightConfig) SpotlightConfig {
	if team.Name == "" {
		team.Name = def.Name
	}
	if team.Author == "" {
		team.Author = def.Author
	}
	if team.Description == "" {
		team.Description = def.Description
	}
	if team.RepoURL == "" {
		team.RepoURL = def.RepoURL
	}
	return team
}
