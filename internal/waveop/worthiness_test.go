package waveop_test

import (
	"testing"

	"github.com/sgrankin/wave/internal/op"
	"github.com/sgrankin/wave/internal/waveop"
)

func annChange(t *testing.T, key string, old, new *string) op.AnnotationBoundaryMap {
	t.Helper()
	m, err := op.NewAnnotationBoundaryMap(nil, []op.AnnotationChange{{Key: key, OldValue: old, NewValue: new}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestIsWorthyChange(t *testing.T) {
	s := func(v string) *string { return &v }
	emptyAttrs, _ := op.NewAttributes(nil)
	update := func() op.AttributesUpdate {
		u, _ := op.NewAttributesUpdate([]op.AttributeChange{{Name: "a", NewValue: s("1")}})
		return u
	}
	cases := []struct {
		name   string
		comps  []op.Component
		worthy bool
	}{
		{"characters", []op.Component{op.Characters{Text: "x"}}, true},
		{"deleteCharacters", []op.Component{op.DeleteCharacters{Text: "x"}}, true},
		{"updateAttributes", []op.Component{op.UpdateAttributes{Update: update()}}, true},
		{"replaceAttributes", []op.Component{op.ReplaceAttributes{OldAttributes: emptyAttrs, NewAttributes: emptyAttrs}}, true},
		{"non-anchor elementStart", []op.Component{op.ElementStart{Type: "p", Attributes: emptyAttrs}, op.ElementEnd{}}, true},
		{"style annotation", []op.Component{op.AnnotationBoundary{Boundary: annChange(t, "style/bold", nil, s("true"))}}, true},
		{"pure retain", []op.Component{op.Retain{Count: 5}}, false},
		{"inline-reply anchor", []op.Component{op.ElementStart{Type: "reply", Attributes: emptyAttrs}, op.ElementEnd{}}, false},
		{"presence annotation", []op.Component{op.AnnotationBoundary{Boundary: annChange(t, "user/r/bob", nil, s("x"))}}, false},
		{"spell annotation", []op.Component{op.AnnotationBoundary{Boundary: annChange(t, "spell/x", nil, s("y"))}}, false},
		{"no-op annotation change", []op.Component{op.AnnotationBoundary{Boundary: annChange(t, "style/bold", s("v"), s("v"))}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := waveop.IsWorthyChange(op.NewDocOp(tc.comps)); got != tc.worthy {
				t.Errorf("IsWorthyChange(%s) = %v, want %v", tc.name, got, tc.worthy)
			}
		})
	}
}

func TestIsWorthyBlipID(t *testing.T) {
	cases := map[string]bool{
		"b+abc":      true,
		"conv+root":  true,
		"attach+1":   false,
		"attachment": false,
		"mini":       false,
		"tr+es":      false,
	}
	for id, want := range cases {
		if got := waveop.IsWorthyBlipID(id); got != want {
			t.Errorf("IsWorthyBlipID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestUpdatesBlipMetadata(t *testing.T) {
	c := ctx(pid(t, "alice@example.com"))
	worthyOp := waveop.BlipContentOperation{Ctx: c, ContentOp: op.NewDocOp([]op.Component{op.Characters{Text: "x"}})}
	if !worthyOp.UpdatesBlipMetadata("b+1") {
		t.Error("worthy edit to a worthy blip should update metadata")
	}
	if worthyOp.UpdatesBlipMetadata("attach+1") {
		t.Error("edit to a system (attachment) document should not update metadata")
	}
	unworthyOp := waveop.BlipContentOperation{Ctx: c, ContentOp: op.NewDocOp([]op.Component{op.Retain{Count: 3}})}
	if unworthyOp.UpdatesBlipMetadata("b+1") {
		t.Error("unworthy edit should not update metadata")
	}
}
