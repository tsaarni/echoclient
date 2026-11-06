package metrics

import (
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"golang.org/x/sys/unix"
)

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	underline   = "\033[4m"
)

// colorsEnabled reports whether output is a terminal and colors are not disabled.
func colorsEnabled(output io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := output.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	return err == nil
}

func tabularDump(output io.Writer, snap *metricSnapshot) {
	useColors := colorsEnabled(output)
	color := func(code string) string {
		if useColors {
			return code
		}
		return ""
	}

	w := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)

	_, _ = fmt.Fprintf(w, "%s%sMetric\tLabels\tValue\t%s\n",
		color(colorGreen+underline), "", color(colorReset))

	for _, e := range snap.entries {
		_, _ = fmt.Fprintf(w, "%s%s%s\t%s\t%v\t\n",
			color(colorYellow), e.name, color(colorReset),
			formatLabelsSorted(e.labels), humanizeMetric(e.name, e.value))
	}

	_ = w.Flush()
}
