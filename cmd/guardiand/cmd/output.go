package cmd

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
)

// Global color definitions for consistent formatting
var (
	headerColor  = color.New(color.FgCyan, color.Bold)
	successColor = color.New(color.FgGreen)
	errorColor   = color.New(color.FgRed)
	noteColor    = color.New(color.FgYellow)
	commandColor = color.New(color.FgGreen, color.Bold)
)

// Indentation constants for consistent formatting
const (
	indent1 = "   "    // 3 spaces - standard indentation
	indent2 = "      " // 6 spaces - double indentation
)

// Message type functions - these cover most use cases

func printHeader(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = headerColor.Println(text)
}

func printSuccess(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = successColor.Printf("✅ %s\n", text)
}

func printError(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = errorColor.Printf("❌ %s\n", text)
}

func printWarning(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = errorColor.Printf("⚠️  %s\n", text)
}

func printNote(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = noteColor.Printf("💡 %s\n", text)
}

func printStep(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = headerColor.Println(text)
}

// Inline content functions (no newline, no prefix)

func printCommand(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = commandColor.Print(text)
}

func printPath(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = headerColor.Print(text)
}

func printValue(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = noteColor.Print(text)
}

func printKey(format string, args ...interface{}) {
	text := fmt.Sprintf(format, args...)
	_, _ = commandColor.Print(text)
}

// Basic printText functions (no styling)

func printText(text string) {
	_, _ = fmt.Print(text)
}

func printf(format string, args ...interface{}) {
	_, _ = fmt.Printf(format, args...)
}

func sprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

func printTextLn(format string, args ...interface{}) {
	fmt.Printf(format+"\n", args...)
}

func printEmptyLine() {
	_, _ = fmt.Println()
}

// Utility functions
func printSeparator(text string) {
	_, _ = headerColor.Println(text)
	separator := strings.Repeat("═", len(stripAnsiCodes(text)))
	_, _ = headerColor.Println(separator)
}
