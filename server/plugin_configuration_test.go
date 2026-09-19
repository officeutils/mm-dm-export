package main

import (
	"testing"
)

type stubConfigurationLoader struct {
	load func(*configuration)
}

func (l stubConfigurationLoader) LoadPluginConfiguration(dest any) error {
	l.load(dest.(*configuration))
	return nil
}

func TestEnableChannelExportDefaultsToFalse(t *testing.T) {
	loader := stubConfigurationLoader{load: func(loaded *configuration) {
		loaded.MaxExportPosts = "37"
	}}

	p := &Plugin{configurationLoader: loader}

	if err := p.OnConfigurationChange(); err != nil {
		t.Fatalf("OnConfigurationChange returned an error: %v", err)
	}

	p.configurationMu.RLock()
	loaded := p.configuration
	p.configurationMu.RUnlock()
	if loaded.EnableChannelExport {
		t.Error("EnableChannelExport = true, want false when omitted")
	}
	if loaded.MaxExportPosts != "37" {
		t.Errorf("MaxExportPosts = %q, want %q", loaded.MaxExportPosts, "37")
	}
	if loaded.ChannelExportAccess != channelExportAdminsOnly {
		t.Errorf("ChannelExportAccess = %q, want %q", loaded.ChannelExportAccess, channelExportAdminsOnly)
	}
}

func TestOnConfigurationChangeLoadsEnableChannelExport(t *testing.T) {
	loader := stubConfigurationLoader{load: func(loaded *configuration) {
		loaded.MaxExportPosts = "37"
		loaded.EnableChannelExport = true
	}}

	p := &Plugin{configurationLoader: loader}

	if err := p.OnConfigurationChange(); err != nil {
		t.Fatalf("OnConfigurationChange returned an error: %v", err)
	}

	p.configurationMu.RLock()
	loaded := p.configuration
	p.configurationMu.RUnlock()
	if !loaded.EnableChannelExport {
		t.Error("EnableChannelExport = false, want true")
	}
	if loaded.MaxExportPosts != "37" {
		t.Errorf("MaxExportPosts = %q, want %q", loaded.MaxExportPosts, "37")
	}
}

func TestOnConfigurationChangeLoadsChannelExportAccess(t *testing.T) {
	for _, tc := range []struct {
		name string
		load string
		want string
	}{
		{name: "all members", load: channelExportAllMembers, want: channelExportAllMembers},
		{name: "admins only", load: channelExportAdminsOnly, want: channelExportAdminsOnly},
		{name: "unknown fails closed", load: "unexpected", want: channelExportAdminsOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plugin{configurationLoader: stubConfigurationLoader{load: func(loaded *configuration) {
				loaded.MaxExportPosts = "37"
				loaded.ChannelExportAccess = tc.load
			}}}
			if err := p.OnConfigurationChange(); err != nil {
				t.Fatalf("OnConfigurationChange returned an error: %v", err)
			}
			if got := p.channelExportAccess(); got != tc.want {
				t.Errorf("channelExportAccess() = %q, want %q", got, tc.want)
			}
		})
	}
}
