package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

type recordingCurrentChannelGetter struct {
	channel            *model.Channel
	err                *model.AppError
	requestedChannelID string
	calls              int
}

func (g *recordingCurrentChannelGetter) GetChannel(channelID string) (*model.Channel, *model.AppError) {
	g.calls++
	g.requestedChannelID = channelID
	return g.channel, g.err
}

func TestExportChannelCommandAcceptsExactCommandAndCurrentContext(t *testing.T) {
	channels := &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: model.ChannelTypeDirect}}
	response, appErr := (&Plugin{
		configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
		currentChannelGetter: channels,
		memberGetter:         validChannelCommandMemberGetter(),
		permissionChecker:    &recordingChannelPermissionChecker{allowed: true},
		postGetter:           validPostGetter(),
		fileGetter:           &recordingFileInfoGetter{},
		exportStore:          validExportStore(),
	}).ExecuteCommand(nil, &model.CommandArgs{
		Command:   "/export-channel",
		UserId:    "requester-id",
		ChannelId: "channel-id",
	})

	if appErr != nil {
		t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
	}
	if response == nil || response.ResponseType != "ephemeral" || response.Text == "" {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response.Text == "Usage: /export-channel" {
		t.Fatalf("exact zero-argument command returned usage: %#v", response)
	}
	if channels.calls != 1 || channels.requestedChannelID != "channel-id" {
		t.Errorf("GetChannel calls = %d with %q, want 1 with channel-id", channels.calls, channels.requestedChannelID)
	}
}

func TestExportChannelCommandAcceptsWhitespaceNormalizedCommandAndCurrentContext(t *testing.T) {
	for _, command := range []string{" /export-channel", "/export-channel ", "\t/export-channel\n"} {
		t.Run(command, func(t *testing.T) {
			channels := &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: model.ChannelTypeDirect}}
			response, appErr := (&Plugin{
				configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
				currentChannelGetter: channels,
				memberGetter:         validChannelCommandMemberGetter(),
				permissionChecker:    &recordingChannelPermissionChecker{allowed: true},
				postGetter:           validPostGetter(),
				fileGetter:           &recordingFileInfoGetter{},
				exportStore:          validExportStore(),
			}).ExecuteCommand(nil, &model.CommandArgs{
				Command:   command,
				UserId:    "requester-id",
				ChannelId: "channel-id",
			})

			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			if response == nil || response.Text == "Usage: /export-channel" || !strings.HasPrefix(response.Text, "[Download your channel export]") {
				t.Fatalf("unexpected response: %#v", response)
			}
			if channels.calls != 1 || channels.requestedChannelID != "channel-id" {
				t.Errorf("GetChannel calls = %d with %q, want 1 with CommandArgs.ChannelId", channels.calls, channels.requestedChannelID)
			}
		})
	}
}

func TestExportDMCommandStillUsesDMPath(t *testing.T) {
	currentChannel := &recordingCurrentChannelGetter{}
	response, appErr := (&Plugin{
		configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
		currentChannelGetter: currentChannel,
		userGetter:           validUserGetter(),
		channelGetter:        validChannelGetter(),
		memberGetter:         validMemberGetter(),
		postGetter:           validPostGetter(),
		exportStore:          validExportStore(),
	}).ExecuteCommand(nil, &model.CommandArgs{Command: "/export-dm @other", UserId: "requester-id", ChannelId: "channel-id"})

	if appErr != nil {
		t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
	}
	if response == nil || !strings.HasPrefix(response.Text, "[Download your direct-message export with @other]") {
		t.Fatalf("unexpected response: %#v", response)
	}
	if currentChannel.calls != 0 {
		t.Errorf("current-channel GetChannel called %d times, want 0", currentChannel.calls)
	}
}

func TestExportChannelCommandRejectsDisabledFeatureBeforeChannelLookup(t *testing.T) {
	channels := &recordingCurrentChannelGetter{}
	response, appErr := (&Plugin{currentChannelGetter: channels}).ExecuteCommand(nil, channelCommandArgs())
	if appErr != nil {
		t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
	}
	if response == nil || response.Text != "Channel export is disabled." {
		t.Fatalf("unexpected response: %#v", response)
	}
	if channels.calls != 0 {
		t.Errorf("GetChannel called %d times, want 0", channels.calls)
	}
}

func TestExportChannelCommandRequiresExactlyTwoDirectParticipants(t *testing.T) {
	tests := []struct {
		name    string
		members model.ChannelMembers
	}{
		{name: "requester only", members: model.ChannelMembers{{ChannelId: "channel-id", UserId: "requester-id"}}},
		{name: "three participants", members: model.ChannelMembers{{ChannelId: "channel-id", UserId: "requester-id"}, {ChannelId: "channel-id", UserId: "target-id"}, {ChannelId: "channel-id", UserId: "third-id"}}},
		{name: "requester absent", members: model.ChannelMembers{{ChannelId: "channel-id", UserId: "first-id"}, {ChannelId: "channel-id", UserId: "second-id"}}},
		{name: "duplicate participant", members: model.ChannelMembers{{ChannelId: "channel-id", UserId: "requester-id"}, {ChannelId: "channel-id", UserId: "requester-id"}}},
		{name: "wrong channel", members: model.ChannelMembers{{ChannelId: "channel-id", UserId: "requester-id"}, {ChannelId: "other-channel", UserId: "target-id"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members := validChannelCommandMemberGetter()
			members.allMembers = tt.members
			posts := validPostGetter()
			response, appErr := (&Plugin{
				configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
				currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: model.ChannelTypeDirect}},
				memberGetter:         members,
				permissionChecker:    &recordingChannelPermissionChecker{allowed: true},
				postGetter:           posts,
			}).ExecuteCommand(nil, channelCommandArgs())
			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			if response.Text != "Unable to export the current channel." {
				t.Errorf("response text = %q", response.Text)
			}
			if posts.calls != 0 {
				t.Errorf("GetPostsForChannel calls = %d, want 0", posts.calls)
			}
		})
	}
}

func TestExportChannelCommandRejectsInvalidParsingAndContextWithoutLookup(t *testing.T) {
	tests := []struct {
		name string
		args *model.CommandArgs
	}{
		{name: "argument", args: &model.CommandArgs{Command: "/export-channel extra", UserId: "requester-id", ChannelId: "channel-id"}},
		{name: "missing user", args: &model.CommandArgs{Command: "/export-channel", ChannelId: "channel-id"}},
		{name: "missing channel", args: &model.CommandArgs{Command: "/export-channel", UserId: "requester-id"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channels := &recordingCurrentChannelGetter{}
			response, appErr := (&Plugin{configuration: configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers}, currentChannelGetter: channels}).ExecuteCommand(nil, tt.args)
			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			if response == nil || response.ResponseType != "ephemeral" || response.Text == "" {
				t.Fatalf("unexpected response: %#v", response)
			}
			if tt.name == "argument" && response.Text != "Usage: /export-channel" {
				t.Errorf("response text = %q, want usage", response.Text)
			}
			if channels.calls != 0 {
				t.Errorf("GetChannel called %d times, want 0", channels.calls)
			}
		})
	}
}

func TestExportChannelCommandRejectsLookupAndIdentityFailures(t *testing.T) {
	lookupError := model.NewAppError("test", "lookup failed", nil, "", 500)
	tests := []struct {
		name    string
		channel *model.Channel
		err     *model.AppError
	}{
		{name: "lookup error", err: lookupError},
		{name: "nil channel"},
		{name: "mismatched ID", channel: &model.Channel{Id: "different-id", Type: model.ChannelTypeOpen}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertChannelCommandRejected(t, &recordingCurrentChannelGetter{channel: tt.channel, err: tt.err})
		})
	}
}

func TestExportChannelCommandAllowsOnlyActiveSupportedChannels(t *testing.T) {
	tests := []struct {
		name    string
		type_   model.ChannelType
		deleted bool
		wantOK  bool
	}{
		{name: "open", type_: model.ChannelTypeOpen, wantOK: true},
		{name: "private", type_: model.ChannelTypePrivate, wantOK: true},
		{name: "direct", type_: model.ChannelTypeDirect, wantOK: true},
		{name: "group", type_: model.ChannelTypeGroup},
		{name: "unknown", type_: model.ChannelType("x")},
		{name: "archived open", type_: model.ChannelTypeOpen, deleted: true},
		{name: "archived private", type_: model.ChannelTypePrivate, deleted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deleteAt := int64(0)
			if tt.deleted {
				deleteAt = 1
			}
			channels := &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: tt.type_, DeleteAt: deleteAt}}
			response := executeChannelCommand(t, channels)
			gotOK := strings.HasPrefix(response.Text, "[Download your channel export]")
			if gotOK != tt.wantOK {
				t.Errorf("response text = %q, accepted = %t, want %t", response.Text, gotOK, tt.wantOK)
			}
		})
	}
}

func TestExportChannelCommandRejectsPublicAndPrivateWithoutMembershipOrReadPermission(t *testing.T) {
	for _, channelType := range []model.ChannelType{model.ChannelTypeOpen, model.ChannelTypePrivate} {
		for _, tc := range []struct {
			name       string
			member     *model.ChannelMember
			permission bool
		}{
			{name: "not a member", permission: true},
			{name: "no read permission", member: &model.ChannelMember{ChannelId: "channel-id", UserId: "requester-id"}},
		} {
			t.Run(string(channelType)+"/"+tc.name, func(t *testing.T) {
				posts := validPostGetter()
				response, appErr := (&Plugin{
					configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
					currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: channelType}},
					memberGetter: &memberLookup{
						members:      map[string]*model.ChannelMember{"requester-id": tc.member},
						memberErrors: map[string]*model.AppError{},
					},
					permissionChecker: &recordingChannelPermissionChecker{allowed: tc.permission},
					postGetter:        posts,
				}).ExecuteCommand(nil, channelCommandArgs())
				if appErr != nil || response.Text != "Unable to export the current channel." {
					t.Fatalf("ExecuteCommand = %#v, %v", response, appErr)
				}
				if posts.calls != 0 {
					t.Errorf("GetPostsForChannel calls = %d, want 0", posts.calls)
				}
			})
		}
	}
}

func TestExportChannelCommandAuthorizesSupportedChannelMembersWithReadPermission(t *testing.T) {
	for _, channelType := range []model.ChannelType{model.ChannelTypeOpen, model.ChannelTypePrivate, model.ChannelTypeDirect} {
		t.Run(string(channelType), func(t *testing.T) {
			members := validChannelCommandMemberGetter()
			permissions := &recordingChannelPermissionChecker{allowed: true}
			response, appErr := (&Plugin{
				configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
				currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: channelType}},
				memberGetter:         members,
				permissionChecker:    permissions,
				postGetter:           validPostGetter(),
				fileGetter:           &recordingFileInfoGetter{},
				exportStore:          validExportStore(),
			}).ExecuteCommand(nil, channelCommandArgs())
			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			if !strings.HasPrefix(response.Text, "[Download your channel export]") {
				t.Fatalf("response text = %q", response.Text)
			}
			if len(members.calls) != 1 || members.calls[0] != "requester-id" {
				t.Errorf("GetChannelMember calls = %v, want [requester-id]", members.calls)
			}
			if len(members.memberChannelIDs) != 1 || members.memberChannelIDs[0] != "channel-id" {
				t.Errorf("GetChannelMember channel IDs = %v, want [channel-id]", members.memberChannelIDs)
			}
			if channelType == model.ChannelTypeDirect && (members.channelID != "channel-id" || members.page != 0 || members.perPage != 3) {
				t.Errorf("GetChannelMembers args = (%q, %d, %d), want (channel-id, 0, 3)", members.channelID, members.page, members.perPage)
			}
			if channelType != model.ChannelTypeDirect && members.channelID != "" {
				t.Errorf("GetChannelMembers called for %s channel", channelType)
			}
			if permissions.calls != 1 || permissions.userID != "requester-id" || permissions.channelID != "channel-id" || permissions.permission != model.PermissionReadChannel {
				t.Errorf("HasPermissionToChannel calls/args = %d, %q, %q, %v", permissions.calls, permissions.userID, permissions.channelID, permissions.permission)
			}
		})
	}
}

func TestExportChannelCommandEnforcesConfiguredAccessMode(t *testing.T) {
	tests := []struct {
		name        string
		access      string
		admin       bool
		member      bool
		read        bool
		enabled     bool
		wantAllowed bool
	}{
		{name: "all members normal member", access: channelExportAllMembers, member: true, read: true, enabled: true, wantAllowed: true},
		{name: "all members non-member", access: channelExportAllMembers, read: true, enabled: true},
		{name: "admins only admin member with read", access: channelExportAdminsOnly, admin: true, member: true, read: true, enabled: true, wantAllowed: true},
		{name: "admins only admin non-member", access: channelExportAdminsOnly, admin: true, read: true, enabled: true},
		{name: "admins only normal member", access: channelExportAdminsOnly, member: true, read: true, enabled: true},
		{name: "admins only admin member without read", access: channelExportAdminsOnly, admin: true, member: true, enabled: true},
		{name: "invalid access fails closed for normal member", access: "invalid", member: true, read: true, enabled: true},
		{name: "disabled ignores all-members mode", access: channelExportAllMembers, member: true, read: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			members := validChannelCommandMemberGetter()
			if !tc.member {
				members.members["requester-id"] = nil
			}
			roles := "system_user"
			if tc.admin {
				roles += " " + model.SystemAdminRoleId
			}
			posts := validPostGetter()
			response, appErr := (&Plugin{
				configuration:        configuration{EnableChannelExport: tc.enabled, ChannelExportAccess: tc.access},
				currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: model.ChannelTypeOpen}},
				memberGetter:         members,
				permissionChecker:    &recordingChannelPermissionChecker{allowed: tc.read},
				userGetter:           &recordingUserGetter{requester: &model.User{Id: "requester-id", Roles: roles}},
				postGetter:           posts,
				fileGetter:           &recordingFileInfoGetter{},
				exportStore:          validExportStore(),
			}).ExecuteCommand(nil, channelCommandArgs())
			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			gotAllowed := strings.HasPrefix(response.Text, "[Download your channel export]")
			if gotAllowed != tc.wantAllowed {
				t.Errorf("response text = %q, allowed = %t, want %t", response.Text, gotAllowed, tc.wantAllowed)
			}
			if !tc.wantAllowed && posts.calls != 0 {
				t.Errorf("GetPostsForChannel calls = %d, want 0", posts.calls)
			}
		})
	}
}

func TestExportChannelCommandRejectsFailedAuthorizationBeforeContentAccess(t *testing.T) {
	lookupError := model.NewAppError("test", "lookup failed", nil, "", 500)
	notFoundError := model.NewAppError("test", "membership not found", nil, "", 404)
	tests := []struct {
		name               string
		channelType        model.ChannelType
		member             *model.ChannelMember
		memberErr          *model.AppError
		permissionAllowed  bool
		wantPermissionCall bool
	}{
		{name: "absent membership", channelType: model.ChannelTypeDirect, memberErr: notFoundError, permissionAllowed: true},
		{name: "failed membership lookup", channelType: model.ChannelTypeDirect, memberErr: lookupError, permissionAllowed: true},
		{name: "nil membership", channelType: model.ChannelTypeDirect, permissionAllowed: true},
		{name: "membership has mismatched channel", channelType: model.ChannelTypeDirect, member: &model.ChannelMember{ChannelId: "other-channel", UserId: "requester-id"}, permissionAllowed: true},
		{name: "membership has mismatched user", channelType: model.ChannelTypeDirect, member: &model.ChannelMember{ChannelId: "channel-id", UserId: "other-user"}, permissionAllowed: true},
		{name: "read permission denied", channelType: model.ChannelTypeDirect, member: &model.ChannelMember{ChannelId: "channel-id", UserId: "requester-id"}, wantPermissionCall: true},
		{name: "administrator without membership", channelType: model.ChannelTypeDirect, permissionAllowed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			members := &memberLookup{
				members:      map[string]*model.ChannelMember{"requester-id": tt.member},
				memberErrors: map[string]*model.AppError{"requester-id": tt.memberErr},
			}
			if tt.member != nil && tt.member.ChannelId == "channel-id" && tt.member.UserId == "requester-id" {
				members.allMembers = validChannelCommandMemberGetter().allMembers
			}
			permissions := &recordingChannelPermissionChecker{allowed: tt.permissionAllowed}
			posts := validPostGetter()
			files := &recordingFileInfoGetter{}
			response, appErr := (&Plugin{
				configuration: configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers}, currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Type: tt.channelType}},
				memberGetter:      members,
				permissionChecker: permissions,
				postGetter:        posts,
				fileGetter:        files,
			}).ExecuteCommand(nil, channelCommandArgs())
			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			if response.Text != "Unable to export the current channel." {
				t.Errorf("response text = %q, want non-disclosing rejection", response.Text)
			}
			wantPermissionCalls := 0
			if tt.wantPermissionCall {
				wantPermissionCalls = 1
			}
			if permissions.calls != wantPermissionCalls {
				t.Errorf("HasPermissionToChannel calls = %d, want %d", permissions.calls, wantPermissionCalls)
			}
			if posts.calls != 0 {
				t.Errorf("GetPostsForChannel calls = %d, want 0", posts.calls)
			}
			if len(files.calls) != 0 {
				t.Errorf("GetFileInfo calls = %v, want none", files.calls)
			}
		})
	}
}

func assertChannelCommandRejected(t *testing.T, channels *recordingCurrentChannelGetter) {
	t.Helper()
	response := executeChannelCommand(t, channels)
	if response.Text != "Unable to export the current channel." {
		t.Errorf("response text = %q, want non-disclosing rejection", response.Text)
	}
}

func executeChannelCommand(t *testing.T, channels *recordingCurrentChannelGetter) *model.CommandResponse {
	t.Helper()
	response, appErr := (&Plugin{
		configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
		currentChannelGetter: channels,
		memberGetter:         validChannelCommandMemberGetter(),
		permissionChecker:    &recordingChannelPermissionChecker{allowed: true},
		postGetter:           validPostGetter(),
		fileGetter:           &recordingFileInfoGetter{},
		exportStore:          validExportStore(),
	}).ExecuteCommand(nil, &model.CommandArgs{
		Command:   "/export-channel",
		UserId:    "requester-id",
		ChannelId: "channel-id",
	})
	if appErr != nil {
		t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
	}
	if response == nil || response.ResponseType != "ephemeral" {
		t.Fatalf("unexpected response: %#v", response)
	}
	return response
}

type channelAuthorGetter struct {
	users map[string]*model.User
	errs  map[string]*model.AppError
	calls []string
}

func (g *channelAuthorGetter) GetUser(id string) (*model.User, *model.AppError) {
	g.calls = append(g.calls, id)
	return g.users[id], g.errs[id]
}

func (*channelAuthorGetter) GetUserByUsername(string) (*model.User, *model.AppError) { return nil, nil }

func TestExportChannelReusesPaginationLimitAttachmentsAndAuthorResolution(t *testing.T) {
	first := fullPostPage("first", postPageSize)
	first.Posts["first-000"].UserId = "author-a"
	first.Posts["first-000"].Message = "from Alice"
	first.Posts["first-000"].FileIds = []string{"file-id"}
	second := postPage("last")
	second.Posts["last"].UserId = "author-b"
	second.Posts["last"].Message = "from Bob"
	posts := &recordingPostGetter{postLists: []*model.PostList{first, second}}
	files := &recordingFileInfoGetter{infos: map[string]*model.FileInfo{"file-id": {Id: "file-id", Name: "notes.txt", Size: 12, MimeType: "text/plain"}}, errs: map[string]*model.AppError{}}
	users := &channelAuthorGetter{users: map[string]*model.User{
		"author-a": {Id: "author-a", Username: "alice"},
		"author-b": {Id: "author-b", Username: "bob"},
	}, errs: map[string]*model.AppError{}}
	store := validExportStore()
	p := &Plugin{
		configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers},
		currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Name: "town-square", DisplayName: "Town Square", Type: model.ChannelTypeDirect}},
		memberGetter:         validChannelCommandMemberGetter(), permissionChecker: &recordingChannelPermissionChecker{allowed: true},
		postGetter: posts, fileGetter: files, userGetter: users, exportStore: store,
		now: func() time.Time { return time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC) },
	}
	p.configuration.MaxExportPosts = "201"

	response, appErr := p.ExecuteCommand(nil, channelCommandArgs())
	if appErr != nil || !strings.HasPrefix(response.Text, "[Download your channel export]") {
		t.Fatalf("ExecuteCommand = %#v, %v", response, appErr)
	}
	if !reflect.DeepEqual(posts.pages, []int{0, 1}) || !reflect.DeepEqual(posts.perPages, []int{200, 200}) {
		t.Errorf("pagination = pages %v sizes %v", posts.pages, posts.perPages)
	}
	if !reflect.DeepEqual(files.calls, []string{"file-id"}) {
		t.Errorf("attachment metadata calls = %v", files.calls)
	}
	if !reflect.DeepEqual(users.calls, []string{"author-a", "author-b"}) {
		t.Errorf("author calls = %v", users.calls)
	}
	html := string(store.contents)
	for _, want := range []string{"Channel: Town Square", "@alice", "@bob", "notes.txt", "Exported 201 messages. Configured limit: 201."} {
		if !strings.Contains(html, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if store.ownerID != "requester-id" || store.filename != "channel-town-square-2026-09-17-120000.html" {
		t.Errorf("stored export owner/file = %q/%q", store.ownerID, store.filename)
	}
}

func TestExportChannelUsesSafeFallbackAuthorLabelsAndKeepsReplies(t *testing.T) {
	list := &model.PostList{Order: []string{"reply", "root", "system"}, Posts: map[string]*model.Post{
		"reply":  {Id: "reply", RootId: "root", UserId: "missing-user", CreateAt: 2, Message: "reply body"},
		"root":   {Id: "root", UserId: "known-user", CreateAt: 1, Message: "root body"},
		"system": {Id: "system", CreateAt: 3, Message: "system body"},
	}}
	users := &channelAuthorGetter{users: map[string]*model.User{"known-user": {Id: "known-user", Username: "known"}}, errs: map[string]*model.AppError{"missing-user": model.NewAppError("test", "gone", nil, "", 404)}}
	store := validExportStore()
	p := &Plugin{configuration: configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers}, currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Name: "channel", Type: model.ChannelTypeDirect}}, memberGetter: validChannelCommandMemberGetter(), permissionChecker: &recordingChannelPermissionChecker{allowed: true}, postGetter: &recordingPostGetter{postList: list}, fileGetter: &recordingFileInfoGetter{}, userGetter: users, exportStore: store}
	p.ExecuteCommand(nil, channelCommandArgs())
	html := string(store.contents)
	for _, want := range []string{"@known", "Unknown user", "System", "root body", "reply body"} {
		if !strings.Contains(html, want) {
			t.Errorf("export missing %q", want)
		}
	}
	if strings.Contains(html, "missing-user") {
		t.Error("unresolved user ID leaked into export")
	}
	assertInOrder(t, html, "root body", "reply body", "system body")
}

func validChannelCommandMemberGetter() *memberLookup {
	requester := model.ChannelMember{ChannelId: "channel-id", UserId: "requester-id"}
	target := model.ChannelMember{ChannelId: "channel-id", UserId: "target-id"}
	return &memberLookup{
		members: map[string]*model.ChannelMember{
			"requester-id": &requester,
		},
		memberErrors: map[string]*model.AppError{},
		allMembers:   model.ChannelMembers{requester, target},
	}
}

func channelCommandArgs() *model.CommandArgs {
	return &model.CommandArgs{Command: "/export-channel", UserId: "requester-id", ChannelId: "channel-id"}
}
