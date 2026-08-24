package httpapi

import (
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"serverdesk/internal/config"
)

func TestConcurrentDeviceMetadataTransactionsKeepRuntimeAndDiskAligned(t *testing.T) {
	fixture := newAdminTestFixture(t, "")
	const writers = 32

	start := make(chan struct{})
	errs := make(chan error, writers)
	var stopReaders atomic.Bool
	var readerWG sync.WaitGroup
	for range 4 {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			<-start
			for !stopReaders.Load() {
				_ = fixture.srv.findEdgeCfg("edge-srv1")
				_ = fixture.srv.edgeCfgCount()
				_ = fixture.edgeMgr.Devices()
			}
		}()
	}

	var writerWG sync.WaitGroup
	for i := range writers {
		writerWG.Add(1)
		go func(index int) {
			defer writerWG.Done()
			<-start
			label := fmt.Sprintf("concurrent-label-%02d", index)
			recorder, _ := execRequest(fixture.srv, http.MethodPut, "/api/clusters/edge-srv1", map[string]any{
				"label": label,
			}, "")
			if recorder.Code != http.StatusOK {
				errs <- fmt.Errorf("writer %d returned %d: %s", index, recorder.Code, recorder.Body.String())
			}
		}(i)
	}
	close(start)
	writerWG.Wait()
	stopReaders.Store(true)
	readerWG.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	loaded, err := config.Load(fixture.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.EdgeDevices) != 1 {
		t.Fatalf("disk edge device count = %d", len(loaded.EdgeDevices))
	}
	runtimeDevice := fixture.srv.findEdgeCfg("edge-srv1")
	if runtimeDevice == nil || runtimeDevice.Name != loaded.EdgeDevices[0].Name {
		t.Fatalf("runtime/disk label mismatch: runtime=%#v disk=%q", runtimeDevice, loaded.EdgeDevices[0].Name)
	}
	workerDevices := fixture.edgeMgr.Devices()
	if len(workerDevices) != 1 || workerDevices[0].Name != loaded.EdgeDevices[0].Name {
		t.Fatalf("worker/disk label mismatch: worker=%#v disk=%q", workerDevices, loaded.EdgeDevices[0].Name)
	}
}
