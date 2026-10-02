package main

import (
	"sync"
	"time"
)

// In testing, a camera gave one Osaio login a single live view at a time. When someone
// watches a camera in the Osaio app with the login BombeCam uses, BombeCam's
// connection succeeds but no video arrives until the app's view closes. The
// stream loop records that here so the viewer can say why, instead of
// "Waiting for the first video frames".
const sameLoginWindow = 2 * time.Minute

var sameLoginViewers sync.Map // camera UUID -> time.Time

func noteSameLoginViewer(uuid string) { sameLoginViewers.Store(uuid, time.Now()) }

func clearSameLoginViewer(uuid string) { sameLoginViewers.Delete(uuid) }

func sameLoginViewerRecently(uuid string) bool {
	v, ok := sameLoginViewers.Load(uuid)
	if !ok {
		return false
	}
	t, _ := v.(time.Time)
	return time.Since(t) < sameLoginWindow
}

const sameLoginDetail = "No video yet: the Osaio app is watching this camera with the same Osaio login BombeCam uses, " +
	"so the camera won't send BombeCam video until that view closes. Give BombeCam its own Osaio login to watch in both at once " +
	"(Add cameras explains how)."
