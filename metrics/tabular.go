package metrics

import (
	"fmt"
	"io"
	"text/tabwriter"
)

const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	underline   = "\033[4m"
)

func tabularDump(output io.Writer, rows []tableRow) {
	w := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "%s%s%sMetric\tLabels\tValue\t%s\n",
		colorGreen, underline, "", colorReset)

	for _, row := range rows {
		fmt.Fprintf(w, "%s%s%s\t%s\t%v\t\n",
			colorYellow, row.metric, colorReset,
			row.labels, row.value)
	}

	w.Flush()
}
