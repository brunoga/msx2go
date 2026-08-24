package trace

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The sites file: places a *running* cartridge went that the trace had not.
//
// Static analysis of a megaROM runs out of certainty long before it runs out
// of program. A bank register written with a value that came from somewhere
// the tracer cannot follow, an indirect jump through a table it did not find,
// a routine that leaves the mapping changed on return -- each of those stops a
// walk, and everything past it is code that will not be translated.
//
// Running the thing settles all of it at once. The generated cartridge panics
// on an address it has no label for, and it knows the paging in force at that
// moment, which is exactly the pair the tracer needs to carry on from. Feed it
// back, regenerate, run again, and each round reaches further than the last.
// It is ground truth rather than inference, and it terminates: there are only
// so many places a program goes.
//
// The file is plain so it can be read, edited and kept beside the cartridge:
//
//	# salamander
//	b8d7 0,7,15,3

// ReadSites loads a sites file. A missing file is not an error -- it is the
// first round.
func ReadSites(path string) ([]State, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []State
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(text, '#'); i >= 0 {
			text = strings.TrimSpace(text[:i])
		}
		if text == "" {
			continue
		}
		fields := strings.Fields(text)
		addr, err := strconv.ParseUint(fields[0], 16, 16)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %q is not an address",
				path, line, fields[0])
		}
		st := State{Addr: uint16(addr), Reason: "seen while running"}
		if len(fields) > 1 {
			for _, b := range strings.Split(fields[1], ",") {
				n, err := strconv.Atoi(b)
				if err != nil {
					return nil, fmt.Errorf(
						"%s:%d: %q is not a bank",
						path, line, b)
				}
				st.Banks = append(st.Banks, n)
			}
		}
		out = append(out, st)
	}
	return out, sc.Err()
}

// AddSite adds a state to a sites file if it is not already there, and reports
// whether it was new. Nothing new means the loop has converged.
func AddSite(path string, st State) (bool, error) {
	have, err := ReadSites(path)
	if err != nil {
		return false, err
	}
	key := siteKey(st)
	for _, h := range have {
		if siteKey(h) == key {
			return false, nil
		}
	}
	have = append(have, st)
	sort.Slice(have, func(i, j int) bool {
		if have[i].Addr != have[j].Addr {
			return have[i].Addr < have[j].Addr
		}
		return siteKey(have[i]) < siteKey(have[j])
	})
	var b strings.Builder
	b.WriteString("# Places the running cartridge went that the trace had " +
		"not. Written by msx2go -discover.\n" +
		"# Each line is an address and the banks that were mapped when " +
		"it was reached.\n")
	for _, h := range have {
		b.WriteString(siteKey(h))
		b.WriteByte('\n')
	}
	return true, os.WriteFile(path, []byte(b.String()), 0o644)
}

func siteKey(st State) string {
	if len(st.Banks) == 0 {
		return fmt.Sprintf("%04x", st.Addr)
	}
	parts := make([]string, len(st.Banks))
	for i, b := range st.Banks {
		parts[i] = strconv.Itoa(b)
	}
	return fmt.Sprintf("%04x %s", st.Addr, strings.Join(parts, ","))
}
