package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// Проверка awg_manager -- источник снимка версий на бэкенде. Без загруженной
// версии модуля бэкенд не узнает, что нужна перезагрузка.
func TestAwgManagerCheck_ReportsLoadedKernelModuleVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":"2.19.0+r2","kernelModuleVersion":"3.2.20260930","kernelModuleLoadedVersion":"3.1.20260906","kernelModuleLoaded":true}}`))
	}))
	defer srv.Close()
	got := AwgManagerCheck{Client: awgmgr.New(srv.URL)}.Run(context.Background(), Deps{})
	if got.Details["kernel_module_loaded_version"] != "3.1.20260906" {
		t.Errorf("details = %+v", got.Details)
	}
	if got.Details["kernel_module_version"] != "3.2.20260930" {
		t.Errorf("установленная версия потерялась: %+v", got.Details)
	}
}
