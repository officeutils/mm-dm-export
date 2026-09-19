package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
)

func TestExecuteCommandBuildsStoredRequesterBoundExportAndReturnsDownloadLink(t *testing.T) {
	store := newMemoryExportStore(time.Minute, 4, 1)
	exportedAt := time.Date(2026, time.September, 16, 12, 34, 56, 0, time.UTC)
	posts := &recordingPostGetter{postList: &model.PostList{
		Order: []string{"post-id"},
		Posts: map[string]*model.Post{"post-id": {
			Id: "post-id", UserId: "requester-id", CreateAt: exportedAt.Add(-time.Minute).UnixMilli(),
			Message: "private message", FileIds: []string{"file-id"},
		}},
	}}
	files := &recordingFileInfoGetter{
		infos: map[string]*model.FileInfo{"file-id": {Id: "file-id", Name: "notes.txt", Size: 42, MimeType: "text/plain"}},
		errs:  map[string]*model.AppError{},
	}
	p := &Plugin{
		userGetter: validUserGetter(), channelGetter: validChannelGetter(),
		memberGetter: validMemberGetter(), postGetter: posts, fileGetter: files,
		exportStore: store, now: func() time.Time { return exportedAt },
	}

	response, appErr := p.ExecuteCommand(nil, &model.CommandArgs{Command: "/export-dm @other", UserId: "requester-id"})
	if appErr != nil {
		t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
	}
	const prefix = "[Download your direct-message export with @other](/plugins/" + pluginID + "/download?token="
	if response.ResponseType != "ephemeral" || !strings.HasPrefix(response.Text, prefix) {
		t.Fatalf("command response = %#v", response)
	}
	tokenAndSuffix := strings.TrimPrefix(response.Text, prefix)
	token, suffix, found := strings.Cut(tokenAndSuffix, ")")
	if !found || token == "" || suffix != ". This one-time link expires in 10 minutes." {
		t.Fatalf("download response has malformed token/link: %q", response.Text)
	}

	request := httptest.NewRequest(http.MethodGet, "/download?token="+token, nil)
	request.Header.Set("Mattermost-User-Id", "requester-id")
	recorder := httptest.NewRecorder()
	p.ServeHTTP(nil, recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status = %d, want %d", recorder.Code, http.StatusOK)
	}
	for _, required := range []string{"Direct messages: @requester and @other", "September 16, 2026 at 12:33:56 UTC", "private message", "notes.txt"} {
		if !strings.Contains(recorder.Body.String(), required) {
			t.Errorf("downloaded export does not contain %q", required)
		}
	}
}

func TestExportChannelCommandDeliversRequesterBoundSingleUseExport(t *testing.T) {
	store := newMemoryExportStore(time.Minute, 4, 1)
	exportedAt := time.Date(2026, time.September, 17, 9, 8, 7, 0, time.UTC)
	posts := &recordingPostGetter{postList: &model.PostList{
		Order: []string{"post-id"},
		Posts: map[string]*model.Post{"post-id": {
			Id: "post-id", UserId: "target-id", CreateAt: exportedAt.Add(-time.Minute).UnixMilli(),
			Message: "channel message", FileIds: []string{"file-id"},
		}},
	}}
	files := &recordingFileInfoGetter{
		infos: map[string]*model.FileInfo{"file-id": {Id: "file-id", Name: "channel-notes.txt", Size: 42, MimeType: "text/plain"}},
		errs:  map[string]*model.AppError{},
	}
	p := &Plugin{
		configuration:        configuration{EnableChannelExport: true, ChannelExportAccess: channelExportAllMembers, MaxExportPosts: "1"},
		currentChannelGetter: &recordingCurrentChannelGetter{channel: &model.Channel{Id: "channel-id", Name: "alice--bob", DisplayName: "Alice & Bob", Type: model.ChannelTypeDirect}},
		memberGetter:         validChannelCommandMemberGetter(),
		permissionChecker:    &recordingChannelPermissionChecker{allowed: true},
		postGetter:           posts,
		fileGetter:           files,
		userGetter: &channelAuthorGetter{users: map[string]*model.User{
			"target-id": {Id: "target-id", Username: "other"},
		}, errs: map[string]*model.AppError{}},
		exportStore: store,
		now:         func() time.Time { return exportedAt },
	}

	response, appErr := p.ExecuteCommand(nil, channelCommandArgs())
	if appErr != nil {
		t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
	}
	const prefix = "[Download your channel export](/plugins/" + pluginID + "/download?token="
	if response.ResponseType != "ephemeral" || !strings.HasPrefix(response.Text, prefix) {
		t.Fatalf("command response = %#v", response)
	}
	tokenAndSuffix := strings.TrimPrefix(response.Text, prefix)
	token, suffix, found := strings.Cut(tokenAndSuffix, ")")
	if !found || token == "" || suffix != ". This one-time link expires in 10 minutes." {
		t.Fatalf("download response has malformed token/link: %q", response.Text)
	}

	wrongOwnerRequest := httptest.NewRequest(http.MethodGet, "/download?token="+token, nil)
	wrongOwnerRequest.Header.Set("Mattermost-User-Id", "intruder-id")
	wrongOwnerResponse := httptest.NewRecorder()
	p.ServeHTTP(nil, wrongOwnerResponse, wrongOwnerRequest)
	if wrongOwnerResponse.Code != http.StatusNotFound {
		t.Fatalf("wrong-owner download status = %d, want %d", wrongOwnerResponse.Code, http.StatusNotFound)
	}

	requesterRequest := httptest.NewRequest(http.MethodGet, "/download?token="+token, nil)
	requesterRequest.Header.Set("Mattermost-User-Id", "requester-id")
	requesterResponse := httptest.NewRecorder()
	p.ServeHTTP(nil, requesterResponse, requesterRequest)
	if requesterResponse.Code != http.StatusOK {
		t.Fatalf("requester download status = %d, want %d", requesterResponse.Code, http.StatusOK)
	}
	if got, want := requesterResponse.Header().Get("Content-Disposition"), `attachment; filename="channel-alice--bob-2026-09-17-090807.html"`; got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	for _, required := range []string{"Channel: Alice &amp; Bob", "@other", "channel message", "channel-notes.txt", "Exported 1 messages. Configured limit: 1."} {
		if !strings.Contains(requesterResponse.Body.String(), required) {
			t.Errorf("downloaded export does not contain %q", required)
		}
	}

	replayResponse := httptest.NewRecorder()
	p.ServeHTTP(nil, replayResponse, requesterRequest)
	if replayResponse.Code != http.StatusNotFound {
		t.Errorf("replay status = %d, want %d", replayResponse.Code, http.StatusNotFound)
	}
}

func TestExecuteCommandReportsDeliveryFailuresEphemerally(t *testing.T) {
	base := func(store temporaryExportStore) *Plugin {
		return &Plugin{
			userGetter: validUserGetter(), channelGetter: validChannelGetter(),
			memberGetter: validMemberGetter(), postGetter: validPostGetter(), exportStore: store,
		}
	}

	for _, test := range []struct {
		name  string
		store temporaryExportStore
		want  string
	}{
		{name: "missing store", want: "Export delivery is temporarily unavailable."},
		{name: "store rejection", store: &recordingExportStore{putErr: errOwnerCapacity}, want: "Unable to store that direct-message export. Please download any existing export or try again later."},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, appErr := base(test.store).ExecuteCommand(nil, &model.CommandArgs{Command: "/export-dm @other", UserId: "requester-id"})
			if appErr != nil {
				t.Fatalf("ExecuteCommand returned an AppError: %v", appErr)
			}
			if response.ResponseType != "ephemeral" || response.Text != test.want {
				t.Errorf("response = %#v, want text %q", response, test.want)
			}
		})
	}
}
