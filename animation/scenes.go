package animation

import (
	"fmt"
	"math/rand"
)

// glyph is the unit of motion. Each has a final rune at (endX, endY) and a
// randomized scatter position (startX, startY) used during scatter→condense
// (and reversed for condense→scatter).
type glyph struct {
	ch          rune
	style       string
	startX      float64
	startY      float64
	endX        float64
	endY        float64
	flickerSeed int64
}

// scatterRunes is the decoy character pool glyphs flicker through while in
// transit. Picked to read as code-y noise against Tokyo Night.
var scatterRunes = []rune{
	'.', ',', ';', ':', '*', '+', '~', '/', '\\', '|', '_',
	'#', '$', '%', '&', '?', '<', '>', '=', '^',
}

// flickerAt returns a decoy rune for a glyph at a given frame. Stable per
// (seed, frame/2) so noise looks intentional, not pixel churn.
func flickerAt(seed int64, frame int) rune {
	r := rand.New(rand.NewSource(seed + int64(frame/2)))
	return scatterRunes[r.Intn(len(scatterRunes))]
}

// scatterLayout writes randomized start positions for every glyph, covering
// the grid without clipping borders.
func scatterLayout(glyphs []glyph, width, height int, seed int64) {
	r := rand.New(rand.NewSource(seed))
	for i := range glyphs {
		glyphs[i].startX = float64(r.Intn(width))
		glyphs[i].startY = float64(r.Intn(height))
		glyphs[i].flickerSeed = r.Int63()
	}
}

// centerText computes the target (x, y) for a centered single-line label.
func centerText(s string, width, height int) (int, int) {
	x := (width - len([]rune(s))) / 2
	if x < 0 {
		x = 0
	}
	y := height / 2
	return x, y
}

// hashSeed produces a stable int64 from a string (FNV-1a 64, inlined).
func hashSeed(s string) int64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	var h uint64 = offset64
	for _, b := range []byte(s) {
		h ^= uint64(b)
		h *= prime64
	}
	return int64(h)
}

// =============================================================================
// WelcomeScene
//
// Phases for a ~2s welcome at 30fps (60 frames):
//   - 0..30  scatter→condense: glyphs travel from random start to centered title
//   - 30..36 underline flourish draws beneath the title
//   - 36..54 hold steady
//   - 54..60 settle: title fades to muted, "system: @nick joined" appears at
//            the bottom as a chat-line.
// =============================================================================

// WelcomeScene materializes a candidate nick from scattered glyphs into a
// centered title, draws an underline, holds, then settles into a system
// chat-line at the bottom.
type WelcomeScene struct {
	width, height int
	nick          string
	glyphs        []glyph
	frames        int
}

// NewWelcomeScene returns an uninitialized welcome scene. Call Init with
// params {"nick": "@bogdan_chayka"} before use.
func NewWelcomeScene() *WelcomeScene { return &WelcomeScene{} }

func (s *WelcomeScene) Init(width, height int, params map[string]any) error {
	if width < 24 || height < 6 {
		return fmt.Errorf("welcome scene: grid too small (%dx%d)", width, height)
	}
	nick, _ := params["nick"].(string)
	if nick == "" {
		nick = "@guest"
	}
	s.width = width
	s.height = height
	s.nick = nick
	s.frames = 60

	runes := []rune(nick)
	cx, cy := centerText(nick, width, height)

	s.glyphs = make([]glyph, len(runes))
	for i, r := range runes {
		s.glyphs[i] = glyph{
			ch:    r,
			style: "accent2",
			endX:  float64(cx + i),
			endY:  float64(cy),
		}
	}
	scatterLayout(s.glyphs, width, height, hashSeed(nick))
	return nil
}

func (s *WelcomeScene) FrameCount() int { return s.frames }
func (s *WelcomeScene) DurationMs() int { return s.frames * FrameMs }

func (s *WelcomeScene) Frame(i int) FrameData {
	f := blankFrame(s.width, s.height)

	const (
		condenseEnd = 30
		flourishEnd = 36
		holdEnd     = 54
		settleEnd   = 60
	)

	cx, cy := centerText(s.nick, s.width, s.height)
	underlineY := cy + 1
	nickLen := len([]rune(s.nick))

	switch {
	case i < condenseEnd:
		t := easeInOutCubic(float64(i) / float64(condenseEnd))
		for _, g := range s.glyphs {
			x := roundI(lerp(g.startX, g.endX, t))
			y := roundI(lerp(g.startY, g.endY, t))
			ch := g.ch
			style := g.style
			if t < 0.85 {
				ch = flickerAt(g.flickerSeed, i)
				style = "dim"
				if t > 0.5 {
					style = "accent"
				}
			}
			f.putRune(x, y, ch, style)
		}

	case i < flourishEnd:
		for _, g := range s.glyphs {
			f.putRune(int(g.endX), int(g.endY), g.ch, "accent2")
		}
		progress := float64(i-condenseEnd) / float64(flourishEnd-condenseEnd)
		drawCols := roundI(float64(nickLen) * easeInOutCubic(progress))
		for x := 0; x < drawCols; x++ {
			f.putRune(cx+x, underlineY, '─', "accent")
		}

	case i < holdEnd:
		for _, g := range s.glyphs {
			f.putRune(int(g.endX), int(g.endY), g.ch, "accent2")
		}
		for x := 0; x < nickLen; x++ {
			f.putRune(cx+x, underlineY, '─', "accent")
		}

	default:
		t := float64(i-holdEnd) / float64(settleEnd-holdEnd)
		titleStyle := "accent2"
		ulStyle := "accent"
		if t > 0.4 {
			titleStyle = "dim"
			ulStyle = "muted"
		}
		for _, g := range s.glyphs {
			f.putRune(int(g.endX), int(g.endY), g.ch, titleStyle)
		}
		for x := 0; x < nickLen; x++ {
			f.putRune(cx+x, underlineY, '─', ulStyle)
		}
		if t > 0.15 {
			line := fmt.Sprintf("system: %s joined", s.nick)
			runes := []rune(line)
			lx := (s.width - len(runes)) / 2
			ly := s.height - 2
			revealT := easeInOutCubic((t - 0.15) / 0.6)
			if revealT > 1 {
				revealT = 1
			}
			revealCount := roundI(float64(len(runes)) * revealT)
			prefix := []rune("system: ")
			for k := 0; k < revealCount && k < len(runes); k++ {
				style := "dim"
				if k >= len(prefix) && k < len(prefix)+nickLen {
					style = "accent2"
				}
				f.putRune(lx+k, ly, runes[k], style)
			}
		}
	}
	return f
}

// =============================================================================
// TransitionScene
//
// 1.0s wipe at 30fps (30 frames):
//   - 0..18  outgoing glyphs travel from centered "from" text to scatter, fading
//            accent2 → accent → dim → muted.
//   - 12..30 incoming glyphs travel from a fresh scatter to centered "to" text,
//            materializing dim → accent → accent2.
// Phases overlap by 6 frames so cards cross without an empty beat.
// =============================================================================

// TransitionScene crossfades two card titles via scatter motion.
type TransitionScene struct {
	width, height int
	from, to      string
	out           []glyph
	in            []glyph
	frames        int
}

// NewTransitionScene returns an uninitialized transition. Call Init with
// {"from": "...", "to": "..."} before use.
func NewTransitionScene() *TransitionScene { return &TransitionScene{} }

func (s *TransitionScene) Init(width, height int, params map[string]any) error {
	if width < 24 || height < 5 {
		return fmt.Errorf("transition scene: grid too small (%dx%d)", width, height)
	}
	from, _ := params["from"].(string)
	to, _ := params["to"].(string)
	if from == "" {
		from = "..."
	}
	if to == "" {
		to = "..."
	}
	s.width = width
	s.height = height
	s.from = from
	s.to = to
	s.frames = 30

	fromRunes := []rune(from)
	toRunes := []rune(to)
	fcx, fcy := centerText(from, width, height)
	tcx, tcy := centerText(to, width, height)

	// outgoing: endX/endY centered, startX/startY scattered; travels end→start.
	s.out = make([]glyph, len(fromRunes))
	for i, r := range fromRunes {
		s.out[i] = glyph{
			ch:    r,
			style: "accent2",
			endX:  float64(fcx + i),
			endY:  float64(fcy),
		}
	}
	scatterLayout(s.out, width, height, hashSeed(from+"->out"))

	// incoming: startX/startY scattered, endX/endY centered; travels start→end.
	s.in = make([]glyph, len(toRunes))
	for i, r := range toRunes {
		s.in[i] = glyph{
			ch:    r,
			style: "accent2",
			endX:  float64(tcx + i),
			endY:  float64(tcy),
		}
	}
	scatterLayout(s.in, width, height, hashSeed(to+"->in"))
	return nil
}

func (s *TransitionScene) FrameCount() int { return s.frames }
func (s *TransitionScene) DurationMs() int { return s.frames * FrameMs }

func (s *TransitionScene) Frame(i int) FrameData {
	f := blankFrame(s.width, s.height)

	const (
		outEnd  = 18
		inStart = 12
	)

	if i < outEnd {
		t := easeInOutCubic(float64(i) / float64(outEnd))
		style := "accent2"
		switch {
		case t > 0.75:
			style = "muted"
		case t > 0.5:
			style = "dim"
		case t > 0.25:
			style = "accent"
		}
		for _, g := range s.out {
			x := roundI(lerp(g.endX, g.startX, t))
			y := roundI(lerp(g.endY, g.startY, t))
			ch := g.ch
			if t > 0.4 {
				ch = flickerAt(g.flickerSeed, i)
			}
			f.putRune(x, y, ch, style)
		}
	}

	if i >= inStart {
		t := easeInOutCubic(float64(i-inStart) / float64(s.frames-inStart))
		style := "dim"
		switch {
		case t > 0.85:
			style = "accent2"
		case t > 0.5:
			style = "accent"
		}
		for _, g := range s.in {
			x := roundI(lerp(g.startX, g.endX, t))
			y := roundI(lerp(g.startY, g.endY, t))
			ch := g.ch
			if t < 0.85 {
				ch = flickerAt(g.flickerSeed, i)
			}
			f.putRune(x, y, ch, style)
		}
	}
	return f
}
