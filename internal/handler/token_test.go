package handler

import (
	"net/http/httptest"
	"testing"
)

func TestTokenWithoutAuthIsLoopbackOnly(t *testing.T) {
	remoteReq := httptest.NewRequest("GET", "/token", nil)
	remoteReq.RemoteAddr = "192.0.2.10:1234"
	remoteResp := httptest.NewRecorder()
	Token(remoteResp, remoteReq)
	if remoteResp.Code != 403 {
		t.Fatalf("expected remote request to be forbidden, got %d", remoteResp.Code)
	}

	localReq := httptest.NewRequest("GET", "/token", nil)
	localReq.RemoteAddr = "127.0.0.1:1234"
	localResp := httptest.NewRecorder()
	Token(localResp, localReq)
	if localResp.Code != 200 {
		t.Fatalf("expected loopback request to succeed, got %d", localResp.Code)
	}
}
