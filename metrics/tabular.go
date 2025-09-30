package metrics

import (
	"github.com/fatih/color"
	"github.com/rodaine/table"
)

func tabularDump(rows []tableRow) {
	headerFmt := color.New(color.FgGreen, color.Underline).SprintfFunc()
	columnFmt := color.New(color.FgYellow).SprintfFunc()

	tbl := table.New("Metric", "Labels", "Value")
	tbl.WithHeaderFormatter(headerFmt).WithFirstColumnFormatter(columnFmt)
	for _, row := range rows {
		tbl.AddRow(row.metric, row.labels, row.value)
	}

	tbl.Print()
}
