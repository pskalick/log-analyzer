package main

import (
        "bytes"
        "encoding/json"
        "fmt"
        "io"
        "log"
        "net/http"
        "os"
        "strings"
        "time"
)

const (
        logFilePath        = "/var/log/remote.log"
        outputFile         = "/home/pi/log_summary.txt"
        aiEndpoint         = "http://192.168.0.161:1234/v1/chat/completions"
        modelName          = "qwen2.5-7b-instruct-1m" // Using the model that worked in your last attempt
        maxTokensPerChunk  = 1500                   // Much smaller to stay safely under 4096 limit
        maxCharsPerSummary = 20000                  // Limit final summary size
)

// Very rough token count estimation (1 token ≈ 4 characters for English text)
func estimateTokens(text string) int {
        return len(text) / 4
}

func main() {
        log.Println("Log analyzer starting...")

        // Calculate time range for the last 1 hour (changed from 24 hours)
        endTime := time.Now()
        startTime := endTime.Add(-1 * time.Hour) // Changed to 1 hour instead of 24

        log.Printf("Filtering logs from %s to %s", startTime.Format(time.RFC3339), endTime.Format(time.RFC3339))

        // Read log file
        logData, err := os.ReadFile(logFilePath)
        if err != nil {
                log.Fatalf("Failed to read log file: %v", err)
        }

        // Filter log entries for the last hour
        log.Println("Filtering logs for the last hour...")
        var filteredLogLines []string
        logLines := bytes.Split(logData, []byte("\n"))
        for _, line := range logLines {
                if len(line) > 0 {
                        // Make sure the line is long enough before attempting to parse timestamp
                        if len(line) >= 25 {
                                timeStr := string(line[:25])
                                logTime, err := time.Parse(time.RFC3339, timeStr)
                                if err == nil && logTime.After(startTime) && logTime.Before(endTime) {
                                        filteredLogLines = append(filteredLogLines, string(line))
                                }
                        }
                }
        }

        log.Printf("Found %d log lines in the last hour", len(filteredLogLines))

        // Determine chunk size based on number of lines
        // Much smaller chunks to ensure we stay under context limit
        linesPerChunk := 30 // Start with a conservative number

        // If we have very few lines, process them all at once
        if len(filteredLogLines) <= linesPerChunk {
                linesPerChunk = len(filteredLogLines)
        }

        log.Printf("Processing logs in chunks of %d lines", linesPerChunk)

        var successfulAnalyses []string
        var errorMessages []string

        // Process logs in chunks
        chunkCount := (len(filteredLogLines) + linesPerChunk - 1) / linesPerChunk
        for i := 0; i < len(filteredLogLines); i += linesPerChunk {
                end := i + linesPerChunk
                if end > len(filteredLogLines) {
                        end = len(filteredLogLines)
                }

                chunkLines := filteredLogLines[i:end]
                chunkText := strings.Join(chunkLines, "\n")

                // Check if chunk is too large before processing
                estimatedChunkTokens := estimateTokens(chunkText)
                if estimatedChunkTokens > maxTokensPerChunk {
                        // If too large, reduce chunk size and retry
                        reductionFactor := float64(maxTokensPerChunk) / float64(estimatedChunkTokens)
                        newEnd := i + int(float64(end-i)*reductionFactor)
                        if newEnd <= i {
                                newEnd = i + 1 // Ensure we process at least one line
                        }
                        if newEnd > len(filteredLogLines) {
                                newEnd = len(filteredLogLines)
                        }

                        log.Printf("Chunk %d/%d too large (%d tokens), reducing from %d to %d lines",
                                (i/linesPerChunk)+1, chunkCount, estimatedChunkTokens, end-i, newEnd-i)

                        chunkLines = filteredLogLines[i:newEnd]
                        chunkText = strings.Join(chunkLines, "\n")
                        end = newEnd
                }

                log.Printf("Processing chunk %d/%d (lines %d-%d)",
                        (i/linesPerChunk)+1, chunkCount, i+1, end)

                analysis, isError := processLogChunk(chunkText, fmt.Sprintf("Part %d/%d",
                        (i/linesPerChunk)+1, chunkCount))

                if isError {
                        errorMessages = append(errorMessages, analysis)
                        log.Printf("Error processing chunk %d/%d: %s",
                                (i/linesPerChunk)+1, chunkCount, analysis)
                } else {
                        successfulAnalyses = append(successfulAnalyses, analysis)
                        log.Printf("Successfully processed chunk %d/%d",
                                (i/linesPerChunk)+1, chunkCount)
                }

                // Save progress after each chunk
                saveProgress(successfulAnalyses, errorMessages)
        }

        // If we have multiple successful analyses, create a simple concatenated summary
        // Skip the "final summary" step that was causing problems
        if len(successfulAnalyses) > 0 {
                compileFinalSummary(successfulAnalyses, errorMessages)
        } else {
                log.Println("No successful analyses to summarize")
        }

        log.Printf("Log analysis and recommendations saved to %s", outputFile)
}

func processLogChunk(logText string, chunkLabel string) (string, bool) {
        // Prepare the chat API payload
        requestBody := map[string]interface{}{
                "model": modelName,
                "messages": []map[string]string{
                        {
                                "role":    "system",
                                "content": "You are a log analyzer. Extract the MOST IMPORTANT issues and patterns from the logs. Be concise. Focus only on critical findings.",
                        },
                        {
                                "role":    "user",
                                "content": fmt.Sprintf("Analyze these logs and identify the most important issues. Keep your response SHORT and FOCUSED only on critical findings:\n\n%s", logText),
                        },
                },
                "temperature": 0.3, // Lower temperature for more consistent, focused responses
        }

        requestJSON, err := json.Marshal(requestBody)
        if err != nil {
                errMsg := fmt.Sprintf("Failed to create JSON payload: %v", err)
                return errMsg, true
        }

        // Send the log entries to the AI model for analysis
        resp, err := http.Post(aiEndpoint, "application/json", bytes.NewBuffer(requestJSON))
        if err != nil {
                errMsg := fmt.Sprintf("Failed to send request: %v", err)
                return errMsg, true
        }
        defer resp.Body.Close()

        // Read the response
        body, err := io.ReadAll(resp.Body)
        if err != nil {
                errMsg := fmt.Sprintf("Failed to read response: %v", err)
                return errMsg, true
        }

        // Log raw response for debugging
        log.Printf("Raw response for %s: %s", chunkLabel, string(body))

        // Extract and save the AI analysis
        var result map[string]interface{}
        err = json.Unmarshal(body, &result)
        if err != nil {
                errMsg := fmt.Sprintf("Failed to parse response: %v", err)
                return errMsg, true
        }

        // Check for errors first
        if errorObj, hasError := result["error"].(map[string]interface{}); hasError {
                errorMsg := "Unknown error"
                if msg, ok := errorObj["message"].(string); ok {
                        errorMsg = msg
                }
                return fmt.Sprintf("Error from AI service: %s", errorMsg), true
        } else if errorStr, hasErrorStr := result["error"].(string); hasErrorStr {
                return fmt.Sprintf("Error from AI service: %s", errorStr), true
        }

        // Extract analysis text
        analysis := fmt.Sprintf("No analysis received for %s.", chunkLabel)
        if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
                if choice, ok := choices[0].(map[string]interface{}); ok {
                        if message, ok := choice["message"].(map[string]interface{}); ok {
                                if content, ok := message["content"].(string); ok {
                                        analysis = content
                                }
                        }
                }
        }

        return fmt.Sprintf("=== %s ===\n\n%s", chunkLabel, analysis), false
}

func saveProgress(analyses []string, errors []string) {
        var buffer strings.Builder

        // Add successful analyses
        if len(analyses) > 0 {
                buffer.WriteString("## SUCCESSFUL ANALYSES\n\n")
                for _, analysis := range analyses {
                        buffer.WriteString(analysis)
                        buffer.WriteString("\n\n---\n\n")
                }
        }

        // Add error messages if any
        if len(errors) > 0 {
                buffer.WriteString("\n\n## ERRORS\n\n")
                for _, err := range errors {
                        buffer.WriteString(err)
                        buffer.WriteString("\n\n")
                }
        }

        // Write the analysis to the output file
        err := os.WriteFile(outputFile, []byte(buffer.String()), 0644)
        if err != nil {
                log.Printf("Failed to write output file: %v", err)
        }
}

func compileFinalSummary(analyses []string, errors []string) {
        var buffer strings.Builder

        // Add a simple header
        buffer.WriteString("# LOG ANALYSIS SUMMARY\n")
        buffer.WriteString(fmt.Sprintf("Generated on %s\n\n", time.Now().Format(time.RFC1123)))

        // Add summary of processing
        buffer.WriteString(fmt.Sprintf("Processed %d chunks of logs from the last hour.\n", len(analyses)))
        if len(errors) > 0 {
                buffer.WriteString(fmt.Sprintf("Encountered %d errors during processing.\n", len(errors)))
        }
        buffer.WriteString("\n---\n\n")

        // Add successful analyses (truncated if necessary)
        buffer.WriteString("## DETAILED FINDINGS\n\n")
        totalChars := 0
        for i, analysis := range analyses {
                // Ensure we don't exceed max summary size
                if totalChars+len(analysis) > maxCharsPerSummary {
                        buffer.WriteString(fmt.Sprintf("\n\n*Note: %d additional analyses were truncated due to size limits.*\n",
                                len(analyses)-i))
                        break
                }
                buffer.WriteString(analysis)
                buffer.WriteString("\n\n---\n\n")
                totalChars += len(analysis)
        }

        // Add error messages if any (truncated if necessary)
        if len(errors) > 0 {
                buffer.WriteString("\n\n## ERRORS\n\n")
                for i, err := range errors {
                        // Ensure we don't exceed max summary size
                        if totalChars+len(err) > maxCharsPerSummary {
                                buffer.WriteString(fmt.Sprintf("\n\n*Note: %d additional errors were truncated due to size limits.*\n",
                                        len(errors)-i))
                                break
                        }
                        buffer.WriteString(err)
                        buffer.WriteString("\n\n")
                        totalChars += len(err)
                }
        }

        // Write the analysis to the output file
        err := os.WriteFile(outputFile, []byte(buffer.String()), 0644)
        if err != nil {
                log.Printf("Failed to write output file: %v", err)
        }
}
