package actions

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// spaceDuShell -- крупнейшие каталоги /opt на глубину 2, не выходя за его
// файловую систему. Первая строка после сортировки -- сам /opt, поэтому 11.
const spaceDuShell = "du -x -k -d 2 /opt 2>/dev/null | sort -rn | head -n 11"

const spaceTopMax = 10

// SpaceReport -- место на /opt и куда оно ушло (v0.57, только чтение).
// Отказ -- только когда не прочитался df: без него экрану нечего показать.
// du, споткнувшийся о часть каталогов, -- не отказ: берём, что разобралось.
func SpaceReport(ctx context.Context, exec ExecFunc) (wire.SpaceReport, error) {
	out, err := exec(ctx, "df", "-k", "/opt")
	if err != nil {
		return wire.SpaceReport{}, fmt.Errorf("df /opt: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	free, total, err := parseDfOptOutput(out)
	if err != nil {
		return wire.SpaceReport{}, err
	}
	du, _ := exec(ctx, "sh", "-c", spaceDuShell)
	return wire.SpaceReport{FreeKB: free, TotalKB: total, Top: parseSpaceDu(string(du))}, nil
}

// parseSpaceDu разбирает «КБ<таб>путь» (у busybox -- табуляция, но годится
// и пробел); строки, которые не разобрались, пропускаются. Путь может
// содержать пробелы: режем только по первому разделителю.
func parseSpaceDu(out string) []wire.SpaceEntry {
	top := []wire.SpaceEntry{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		i := strings.IndexAny(line, "\t ")
		if i <= 0 {
			continue
		}
		kb, err := strconv.ParseInt(line[:i], 10, 64)
		path := strings.TrimSpace(line[i+1:])
		if err != nil || kb < 0 || !strings.HasPrefix(path, "/opt/") {
			continue
		}
		top = append(top, wire.SpaceEntry{Path: path, KB: kb})
		if len(top) == spaceTopMax {
			break
		}
	}
	return top
}
