package main

import "testing"

func TestParseMigrateArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    migrateCmd
		wantErr bool
	}{
		{"up", []string{"up"}, migrateCmd{action: "up"}, false},
		{"version", []string{"version"}, migrateCmd{action: "version"}, false},
		{"down defaults to one step", []string{"down"}, migrateCmd{action: "down", steps: 1}, false},
		{"down with steps", []string{"down", "3"}, migrateCmd{action: "down", steps: 3}, false},
		{"no action", nil, migrateCmd{}, true},
		{"unknown action", []string{"sideways"}, migrateCmd{}, true},
		{"up with extra argument", []string{"up", "2"}, migrateCmd{}, true},
		{"version with extra argument", []string{"version", "x"}, migrateCmd{}, true},
		{"down zero steps", []string{"down", "0"}, migrateCmd{}, true},
		{"down negative steps", []string{"down", "-2"}, migrateCmd{}, true},
		{"down non-numeric steps", []string{"down", "all"}, migrateCmd{}, true},
		{"down too many arguments", []string{"down", "1", "2"}, migrateCmd{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMigrateArgs(tt.args)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseMigrateArgs(%q) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("parseMigrateArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	if err := run([]string{"serve-please"}); err == nil {
		t.Error("run with an unknown command: want error, got nil")
	}
}
