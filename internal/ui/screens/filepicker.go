package screens

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	fpTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#7C3AED")).
		MarginBottom(1)

	fpDir = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	fpFile = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#D1D5DB"))

	fpSelected = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7C3AED")).
			Bold(true)

	fpPath = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6B7280")).
		Italic(true)

	fpKey = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#22D3EE")).
		Bold(true)

	fpKeyDesc = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B7280"))
)

// FileSelectedMsg is emitted when the user selects a file.
type FileSelectedMsg struct {
	Path    string
	Address string // peer to send to
}

type fileEntry struct {
	Name  string
	IsDir bool
	Size  int64
}

// FilePicker lets the user browse the filesystem and select a file.
type FilePicker struct {
	PeerAddress string // who we're sending to
	PeerAlias   string
	Dir         string
	Entries     []fileEntry
	Cursor      int
	Width       int
	Height      int
	err         error
}

// NewFilePicker creates a file picker starting at the user's home directory.
func NewFilePicker(peerAddr, peerAlias string) FilePicker {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/"
	}
	fp := FilePicker{
		PeerAddress: peerAddr,
		PeerAlias:   peerAlias,
		Dir:         home,
	}
	fp.loadDir()
	return fp
}

func (fp *FilePicker) loadDir() {
	entries, err := os.ReadDir(fp.Dir)
	if err != nil {
		fp.err = err
		fp.Entries = nil
		return
	}

	fp.err = nil
	fp.Entries = nil
	fp.Cursor = 0

	// Parent directory entry
	if fp.Dir != "/" {
		fp.Entries = append(fp.Entries, fileEntry{Name: "..", IsDir: true})
	}

	// Sort: directories first, then files
	var dirs, files []fileEntry
	for _, e := range entries {
		// Skip hidden files
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fe := fileEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
		}
		if e.IsDir() {
			dirs = append(dirs, fe)
		} else {
			files = append(files, fe)
		}
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

	fp.Entries = append(fp.Entries, dirs...)
	fp.Entries = append(fp.Entries, files...)
}

// Init returns no command.
func (fp FilePicker) Init() tea.Cmd {
	return nil
}

// Update handles key events.
func (fp FilePicker) Update(msg tea.Msg) (FilePicker, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "up", "k":
			if fp.Cursor > 0 {
				fp.Cursor--
			}
		case "down", "j":
			if fp.Cursor < len(fp.Entries)-1 {
				fp.Cursor++
			}
		case "enter":
			if len(fp.Entries) == 0 {
				return fp, nil
			}
			entry := fp.Entries[fp.Cursor]
			if entry.IsDir {
				if entry.Name == ".." {
					fp.Dir = filepath.Dir(fp.Dir)
				} else {
					fp.Dir = filepath.Join(fp.Dir, entry.Name)
				}
				fp.loadDir()
				return fp, nil
			}
			// File selected
			path := filepath.Join(fp.Dir, entry.Name)
			addr := fp.PeerAddress
			return fp, func() tea.Msg {
				return FileSelectedMsg{Path: path, Address: addr}
			}
		}
	}
	return fp, nil
}

// View renders the file picker.
func (fp FilePicker) View() string {
	var b strings.Builder

	name := fp.PeerAlias
	if name == "" {
		name = shortAddr(fp.PeerAddress)
	}
	b.WriteString(fpTitle.Render("Send file to "+name) + "\n")
	b.WriteString(fpPath.Render(fp.Dir) + "\n\n")

	if fp.err != nil {
		b.WriteString(fmt.Sprintf("Error: %v\n", fp.err))
		return b.String()
	}

	// Calculate visible range for scrolling
	maxVisible := fp.Height - 7 // header + path + padding + hints
	if maxVisible < 1 {
		maxVisible = 1
	}

	start := 0
	if fp.Cursor >= maxVisible {
		start = fp.Cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(fp.Entries) {
		end = len(fp.Entries)
	}

	for i := start; i < end; i++ {
		e := fp.Entries[i]
		cursor := "  "
		var display string

		if e.IsDir {
			display = fpDir.Render(e.Name + "/")
		} else {
			display = fpFile.Render(e.Name) + "  " + fpKeyDesc.Render(formatSize(e.Size))
		}

		if i == fp.Cursor {
			cursor = "> "
			if e.IsDir {
				display = fpSelected.Render(e.Name + "/")
			} else {
				display = fpSelected.Render(e.Name) + "  " + fpKeyDesc.Render(formatSize(e.Size))
			}
		}

		fmt.Fprintf(&b, "%s%s\n", cursor, display)
	}

	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%s %s  %s %s  %s %s",
		fpKey.Render("enter"), fpKeyDesc.Render("select/open"),
		fpKey.Render("↑↓"), fpKeyDesc.Render("navigate"),
		fpKey.Render("esc"), fpKeyDesc.Render("cancel"),
	))

	return b.String()
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1024*1024*1024:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1024*1024*1024))
	case bytes >= 1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	case bytes >= 1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
