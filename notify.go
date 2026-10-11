package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"
)

// Notifications nudge without nagging: a reminder when changes have waited
// unrecorded for days (at most once a day), and a digest of last week's
// history (once a week). The CLI decides what is due and remembers what it
// sent, so the panel, a timer or a cron job only has to show the result.

type Notification struct {
	Summary string `json:"summary"`
	Body    string `json:"body"`
}

type notified struct {
	Remind string `json:"remind,omitempty"` // day of the last reminder
	Digest string `json:"digest,omitempty"` // week of the last digest
}

// OldestPending is when the oldest unrecorded change was made, as far as
// file and package times tell; zero when nothing pending has a time.
func (s *Store) OldestPending(pending []Change) time.Time {
	var oldest time.Time
	for _, c := range pending {
		if t, ok := changeTime(s, c); ok && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	return oldest
}

// Notify returns the notifications due now and marks them sent. remindDays 0
// turns the reminder off.
func (s *Store) Notify(now time.Time, remindDays int, digest bool) ([]Notification, error) {
	var sent notified
	if b, err := os.ReadFile(s.path("notified.json")); err == nil {
		json.Unmarshal(b, &sent)
	}
	before := sent
	var out []Notification

	if remindDays > 0 && sent.Remind != now.Format(time.DateOnly) {
		pending, err := s.Pending()
		if err != nil {
			return nil, err
		}
		if oldest := s.OldestPending(pending); !oldest.IsZero() && now.Sub(oldest) >= time.Duration(remindDays)*24*time.Hour {
			var titles []string
			for _, g := range Groups(pending) {
				titles = append(titles, g.Title)
			}
			days := int(now.Sub(oldest).Hours() / 24)
			out = append(out, Notification{
				Summary: fmt.Sprintf("%d system change(s) not recorded for %d days", len(pending), days),
				Body:    strings.Join(titles, ", ") + ". Open System changes in the bar to describe or undo them.",
			})
			sent.Remind = now.Format(time.DateOnly)
		}
	}

	year, week := now.ISOWeek()
	if thisWeek := fmt.Sprintf("%d-W%02d", year, week); digest && sent.Digest != thisWeek {
		// Last week is Monday to Monday, whenever this first runs in the new week.
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		monday := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		history, err := s.History()
		if err != nil {
			return nil, err
		}
		if body := Digest(history, monday.AddDate(0, 0, -7), monday); body != "" {
			out = append(out, Notification{Summary: "Your setup last week", Body: body})
		}
		sent.Digest = thisWeek
	}

	if sent != before {
		b, _ := json.Marshal(sent)
		if err := writeAtomic(s.path("notified.json"), b, 0o600); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Digest sums up the entries recorded in [from, to), or "" when there are none.
func Digest(history []Entry, from, to time.Time) string {
	var entries []Entry
	for _, e := range history {
		if !e.Time.Before(from) && e.Time.Before(to) {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return ""
	}
	apps := map[string]int{}
	var installed, removed, enabled, disabled []string
	authors := map[string]int{}
	undescribed := 0
	changes := 0
	for _, e := range entries {
		if e.Note == "" {
			undescribed++
		}
		by := e.By
		if by == "" {
			by = "you"
		}
		authors[by]++
		for _, c := range e.Changes {
			changes++
			switch {
			case c.Kind == "package" && c.Op == "added":
				installed = append(installed, c.Name)
			case c.Kind == "package":
				removed = append(removed, c.Name)
			case c.Kind == "unit" && c.Op == "added":
				enabled = append(enabled, unitName(c.Name))
			case c.Kind == "unit":
				disabled = append(disabled, unitName(c.Name))
			default:
				_, title := groupOf(c)
				apps[title]++
			}
		}
	}
	lines := []string{fmt.Sprintf("%d entries, %d changes.", len(entries), changes)}
	if len(apps) > 0 {
		names := slices.SortedFunc(maps.Keys(apps), func(a, b string) int {
			if d := apps[b] - apps[a]; d != 0 {
				return d
			}
			return strings.Compare(a, b)
		})
		var parts []string
		for _, n := range names[:min(len(names), 5)] {
			parts = append(parts, fmt.Sprintf("%s (%d)", n, apps[n]))
		}
		if len(names) > 5 {
			parts = append(parts, fmt.Sprintf("%d more", len(names)-5))
		}
		lines = append(lines, "Config: "+strings.Join(parts, ", ")+".")
	}
	for _, l := range []struct {
		label string
		names []string
	}{{"Installed", installed}, {"Removed", removed}, {"Enabled", enabled}, {"Disabled", disabled}} {
		if len(l.names) > 0 {
			lines = append(lines, l.label+": "+shortList(l.names)+".")
		}
	}
	if len(authors) > 1 || authors["you"] == 0 {
		var parts []string
		for _, by := range slices.Sorted(maps.Keys(authors)) {
			parts = append(parts, fmt.Sprintf("%s %d", by, authors[by]))
		}
		lines = append(lines, "By: "+strings.Join(parts, ", ")+".")
	}
	if undescribed > 0 {
		lines = append(lines, fmt.Sprintf("%d without a description.", undescribed))
	}
	return strings.Join(lines, "\n")
}

// unitName drops the scope of a unit line: "user foo.service" -> "foo.service".
func unitName(line string) string {
	_, name, ok := strings.Cut(line, " ")
	if !ok {
		return line
	}
	return name
}

func shortList(names []string) string {
	if len(names) <= 6 {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:6], ", ") + fmt.Sprintf(" and %d more", len(names)-6)
}
