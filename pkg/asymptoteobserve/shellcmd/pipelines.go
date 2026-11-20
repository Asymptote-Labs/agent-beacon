package shellcmd

import (
	"cmp"
	"slices"
	"sort"
)

// pipelineIndex indexes a list of commands by pipeline, once: every question about the other
// stages of a command's pipeline (what the stage before it writes, whether a later stage takes
// its output, which commands are at earlier and later stages) asks it and never scans the
// list, which for every command of a line would be the square of the line's commands.
type pipelineIndex struct {
	commands []Command
	// order are the indexes of the commands by Pipeline, then Stage, then list order; the
	// commands of one pipeline are in the list in order of stage, so each pipeline's run of
	// order is also in list order. first and end bound, for each command, the run of its
	// pipeline in order.
	order, first, end []int
}

// indexPipelines indexes commands by pipeline.
func indexPipelines(commands []Command) *pipelineIndex {
	n := len(commands)
	all := make([]int, 3*n)
	x := &pipelineIndex{commands: commands, order: all[:n:n], first: all[n : 2*n : 2*n], end: all[2*n:]}
	for i := range x.order {
		x.order[i] = i
	}
	slices.SortStableFunc(x.order, func(a, b int) int {
		if c := cmp.Compare(commands[a].Pipeline, commands[b].Pipeline); c != 0 {
			return c
		}
		return cmp.Compare(commands[a].Stage, commands[b].Stage)
	})
	for k := 0; k < n; {
		end := k + 1
		for end < n && commands[x.order[end]].Pipeline == commands[x.order[k]].Pipeline {
			end++
		}
		for j := k; j < end; j++ {
			x.first[x.order[j]], x.end[x.order[j]] = k, end
		}
		k = end
	}
	return x
}

// stageRange returns where in order the commands at stage s of command i's pipeline are.
func (x *pipelineIndex) stageRange(i, s int) (lo, hi int) {
	first, end := x.first[i], x.end[i]
	lo = first + sort.Search(end-first, func(k int) bool { return x.commands[x.order[first+k]].Stage >= s })
	hi = lo + sort.Search(end-lo, func(k int) bool { return x.commands[x.order[lo+k]].Stage > s })
	return lo, hi
}

// at returns the commands at stage s of command i's pipeline, in list order.
func (x *pipelineIndex) at(i, s int) []int {
	lo, hi := x.stageRange(i, s)
	return x.order[lo:hi]
}

// last reports whether no later stage of command i's pipeline takes its output.
func (x *pipelineIndex) last(i int) bool {
	return x.commands[x.order[x.end[i]-1]].Stage == x.commands[i].Stage
}

// earlierLater returns the commands at earlier and at later stages of command i's pipeline,
// in list order; each is nil when there is none. They share the index's memory.
func (x *pipelineIndex) earlierLater(i int) (earlier, later []int) {
	lo, hi := x.stageRange(i, x.commands[i].Stage)
	if first := x.first[i]; lo > first {
		earlier = x.order[first:lo:lo]
	}
	if end := x.end[i]; hi < end {
		later = x.order[hi:end:end]
	}
	return earlier, later
}
