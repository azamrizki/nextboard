package project

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Badges are short coloured labels ("PROD" on red) shown on a project's card and
// next to its name (docs/API-board.md §1). Like groups they are one shared list,
// made once and given to many projects; a project has at most one.

const (
	// BadgesFile sits next to projects.json.
	BadgesFile = "project-badges.json"
	// MaxBadgeName is short because a badge sits on a card cover.
	MaxBadgeName = 12
	// MaxBadgeIcon is the length of an icon name of the web's icon set.
	MaxBadgeIcon = 32
	// MaxBadges keeps the picker one short list.
	MaxBadges = 50
)

// BadgeColors are the names the web has a palette for. The hub stores a name, not
// a colour, so each theme can draw it its own way.
var BadgeColors = []string{"red", "orange", "amber", "green", "teal", "blue", "violet", "pink", "gray"}

var (
	ErrBadgeNotFound  = errors.New("badge not found")
	ErrBadgeNameTaken = errors.New("the badge name is already used")
	ErrBadBadgeName   = fmt.Errorf("a badge name is required, at most %d characters without commas", MaxBadgeName)
	ErrBadBadgeColor  = fmt.Errorf("a badge color is one of %s", strings.Join(BadgeColors, ", "))
	ErrBadBadgeIcon   = fmt.Errorf("a badge icon is empty or an icon name of at most %d letters and digits", MaxBadgeIcon)
	ErrTooManyBadges  = fmt.Errorf("there are at most %d badges", MaxBadges)
)

// The hub does not know the icon set, only that a name cannot be anything else.
var badgeIconRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*$`)

// Badge is one label of the shared list.
type Badge struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Icon      string    `json:"icon,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// openBadges loads the list. A hub from before badges has no file: none yet.
func (s *Store) openBadges() error {
	var list []*Badge
	if _, err := s.st.Load(BadgesFile, &list); err != nil {
		return err
	}
	for _, b := range list {
		if b != nil && b.ID != "" {
			s.badges = append(s.badges, b)
		}
	}
	s.repairBadges()
	return nil
}

// repairBadges makes a project whose badge is gone have none: a file edited by
// hand, or a delete stopped between the two files.
func (s *Store) repairBadges() {
	for _, p := range s.list {
		if p.BadgeID != "" && s.badgeLocked(p.BadgeID) == nil {
			p.BadgeID = ""
		}
	}
}

func (s *Store) saveBadgesLocked() error {
	out := make([]*Badge, len(s.badges))
	copy(out, s.badges)
	return s.st.Save(BadgesFile, out)
}

// CleanBadgeName trims and checks a badge name. Commas are refused so a name can
// stand in a list the way tags do.
func CleanBadgeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxBadgeName || strings.Contains(name, ",") || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", ErrBadBadgeName
	}
	return name, nil
}

func checkBadgeColor(color string) error {
	if !slices.Contains(BadgeColors, color) {
		return ErrBadBadgeColor
	}
	return nil
}

func checkBadgeIcon(icon string) error {
	if icon != "" && (len(icon) > MaxBadgeIcon || !badgeIconRe.MatchString(icon)) {
		return ErrBadBadgeIcon
	}
	return nil
}

// Badges returns the whole list, by name.
func (s *Store) Badges() []Badge {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Badge, 0, len(s.badges))
	for _, b := range s.badges {
		out = append(out, *b)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (s *Store) badgeLocked(id string) *Badge {
	for _, b := range s.badges {
		if b.ID == id {
			return b
		}
	}
	return nil
}

func (s *Store) badgeNameTakenLocked(name, except string) bool {
	for _, b := range s.badges {
		if b.ID != except && strings.EqualFold(b.Name, name) {
			return true
		}
	}
	return false
}

// CreateBadge adds a badge to the list.
func (s *Store) CreateBadge(name, color, icon string) (Badge, error) {
	name, err := CleanBadgeName(name)
	if err != nil {
		return Badge{}, err
	}
	if err := checkBadgeColor(color); err != nil {
		return Badge{}, err
	}
	if err := checkBadgeIcon(icon); err != nil {
		return Badge{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.badges) >= MaxBadges {
		return Badge{}, ErrTooManyBadges
	}
	if s.badgeNameTakenLocked(name, "") {
		return Badge{}, ErrBadgeNameTaken
	}
	b := &Badge{ID: randomID("b_"), Name: name, Color: color, Icon: icon, CreatedAt: time.Now().UTC()}
	s.badges = append(s.badges, b)
	return *b, s.saveBadgesLocked()
}

// UpdateBadge changes what is given. Nil leaves a field alone; icon "" removes it.
// The projects keep it by id, so none of them changes.
func (s *Store) UpdateBadge(id string, name, color, icon *string) (Badge, error) {
	var clean string
	var err error
	if name != nil {
		if clean, err = CleanBadgeName(*name); err != nil {
			return Badge{}, err
		}
	}
	if color != nil {
		if err := checkBadgeColor(*color); err != nil {
			return Badge{}, err
		}
	}
	if icon != nil {
		if err := checkBadgeIcon(*icon); err != nil {
			return Badge{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.badgeLocked(id)
	if b == nil {
		return Badge{}, ErrBadgeNotFound
	}
	if name != nil && s.badgeNameTakenLocked(clean, id) {
		return Badge{}, ErrBadgeNameTaken
	}
	old := *b
	if name != nil {
		b.Name = clean
	}
	if color != nil {
		b.Color = *color
	}
	if icon != nil {
		b.Icon = *icon
	}
	if err := s.saveBadgesLocked(); err != nil {
		*b = old
		return Badge{}, err
	}
	return *b, nil
}

// DeleteBadge removes a badge and takes it off every project that had it. The
// ids of those projects are returned, for their events.
func (s *Store) DeleteBadge(id string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.badgeLocked(id) == nil {
		return nil, ErrBadgeNotFound
	}
	cleared := []string{}
	for _, p := range s.list {
		if p.BadgeID == id {
			p.BadgeID = ""
			cleared = append(cleared, p.ID)
		}
	}
	s.badges = slices.DeleteFunc(s.badges, func(x *Badge) bool { return x.ID == id })
	// The projects first: stopped between the two files, the badge left over has
	// no project, and the next start would repair the opposite anyway.
	if err := s.saveLocked(); err != nil {
		return cleared, err
	}
	return cleared, s.saveBadgesLocked()
}
