// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package search

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCallers(t *testing.T) {
	e := newTestEngine(t)
	r, err := e.Callers(context.Background(), "onCreate", "aosp", "core/java/android/app/Activity.java")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Callers {
		var lines []string
		for _, s := range c.Calls {
			lines = append(lines, fmt.Sprint(s.Line))
		}
		got = append(got, fmt.Sprintf("%s:%s@%d[%s]", c.Path, c.Name, c.Line, strings.Join(lines, ",")))
	}
	// The definition in Activity.java is not a call; the call in
	// ActivityThread.handleLaunch is.
	if strings.Join(got, " ") != "core/java/android/app/ActivityThread.java:handleLaunch@4[6]" {
		t.Errorf("callers of onCreate: %v", got)
	}

	r, _ = e.Callers(context.Background(), "NewModule", "aosp", "tools/build.go")
	if len(r.Callers) != 1 || r.Callers[0].Name != "TestNewModule" || r.NumCalls != 1 {
		t.Errorf("callers of NewModule: %+v", r.Callers)
	}
	r, _ = e.Callers(context.Background(), "intersect", "aosp", "native/libs/ui/Rect.cpp")
	if len(r.Callers) != 0 {
		t.Errorf("callers of intersect (declarations only): %+v", r.Callers[0])
	}
}

func TestCallees(t *testing.T) {
	e := newTestEngine(t)
	r, err := e.Callees(context.Background(), "handleLaunch", "aosp", "core/java/android/app/ActivityThread.java", 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range r.Callees {
		got = append(got, fmt.Sprintf("%s@%d->%s:%d", c.Name, c.Line, c.Path, c.DefLine))
	}
	// "new Activity()" is a constructor call; onCreate resolves to its definition.
	if strings.Join(got, " ") != "Activity@5->core/java/android/app/Activity.java:7 onCreate@6->core/java/android/app/Activity.java:10" {
		t.Errorf("callees of handleLaunch: %v", got)
	}
	r, _ = e.Callees(context.Background(), "onCreate", "aosp", "core/java/android/app/Activity.java", 10)
	got = nil
	for _, c := range r.Callees {
		got = append(got, c.Name)
	}
	if strings.Join(got, " ") != "Window d" {
		t.Errorf("callees of onCreate: %v", got)
	}
	if _, err := e.Callees(context.Background(), "nothere", "aosp", "tools/build.go", 0); err == nil {
		t.Error("callees of an undefined function")
	}
}
