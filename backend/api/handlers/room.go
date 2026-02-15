package handlers

import (
	"net/http"
	"strings"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/store"
)

type roomModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &roomModule{deps: deps}
	})
}

func (m *roomModule) Prefix() string {
	return m.deps.Config.APIBase + "/room"
}

func (m *roomModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "", Summary: "Get room setting", Handler: m.getRoom},
		{Method: http.MethodPost, Pattern: "/update", Summary: "Update room basic settings", Handler: m.updateRoom},
		{Method: http.MethodPost, Pattern: "/announcement", Summary: "Update room announcement", Handler: m.updateAnnouncement},
		{Method: http.MethodGet, Pattern: "/areas", Summary: "List room areas", Handler: m.listAreas},
	}
}

func (m *roomModule) getRoom(w http.ResponseWriter, r *http.Request) {
	if liveInfo, err := m.deps.Bilibili.GetMyLiveRoomInfo(r.Context()); err == nil {
		_, _ = m.deps.Store.UpdateLiveSetting(r.Context(), store.RoomInfoUpdateRequest{
			AreaID:   liveInfo.AreaV2ID,
			RoomName: liveInfo.Title,
			RoomID:   liveInfo.RoomID,
		})
		if strings.TrimSpace(liveInfo.Announce.Content) != "" {
			_, _ = m.deps.Store.UpdateLiveAnnouncement(r.Context(), store.RoomNewUpdateRequest{
				RoomID:  liveInfo.RoomID,
				Content: liveInfo.Announce.Content,
			})
		}
	}
	room, err := m.deps.Store.GetLiveSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, room)
}

func (m *roomModule) updateRoom(w http.ResponseWriter, r *http.Request) {
	var req store.RoomInfoUpdateRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.AreaID <= 0 {
		httpapi.Error(w, -1, "areaId must be greater than 0", http.StatusOK)
		return
	}
	if strings.TrimSpace(req.RoomName) == "" {
		httpapi.Error(w, -1, "roomName is required", http.StatusOK)
		return
	}
	if req.RoomID <= 0 {
		httpapi.Error(w, -1, "roomId is required", http.StatusOK)
		return
	}
	if err := m.deps.Bilibili.UpdateLiveRoomInfo(r.Context(), req.RoomID, req.RoomName, req.AreaID); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	updated, err := m.deps.Store.UpdateLiveSetting(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, updated)
}

func (m *roomModule) updateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req store.RoomNewUpdateRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if len([]rune(req.Content)) > 60 {
		httpapi.Error(w, -1, "content length must be <= 60", http.StatusOK)
		return
	}
	if err := m.deps.Bilibili.UpdateRoomNews(r.Context(), req.RoomID, req.Content); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	updated, err := m.deps.Store.UpdateLiveAnnouncement(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, updated)
}

func (m *roomModule) listAreas(w http.ResponseWriter, r *http.Request) {
	areas, err := m.deps.Bilibili.GetLiveAreas(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, areas)
}
