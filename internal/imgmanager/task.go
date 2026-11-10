package imgmanager

import (
	"log/slog"
)

type Task struct {
	NodeImageSetName string
	Ref              string
}

func (t Task) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("ref", t.Ref),
		slog.String("nodeImageSet", t.NodeImageSetName),
	)
}
