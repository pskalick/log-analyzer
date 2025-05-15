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
        summaryFilePath = "/home/pi/log_summary.txt"
        outputFilePath  = "/home/pi/log_recommendations.txt"
        aiEndpoint      = "http://192.168.0.161:1234/v1/chat/completions"
        modelName       = "qwen2.5-7b-instruct-1m"
)

func main() {
        log.Println("Log summary enhancer starting...")

        // Read the log summary file
        summaryData, err := os.ReadFile(summaryFilePath)
        if err != nil {
                log.Fatalf("Failed to read summary file: %v", err)
        }

        log.Printf("Read %d bytes from summary file", len(summaryData))

        // Check if file is too large - set a reasonable limit
        if len(summaryData) > 100000 {
                log.Println("Summary file is very large, truncating to last 100,000 bytes")
                if len(summaryData) > 100000 {
                        summaryData = summaryData[len(summaryData)-100000:]
                        // Find the first newline to ensure we start at a complete line
                        for i := 0; i < 1000 && i < len(summaryData); i++ {
                                if summaryData[i] == '\n' {
                                        summaryData = summaryData[i+1:]
                                        break
                                }
                        }
                }
        }

        // Send to LLM for enhancement with recommendations
        enhancedSummary, err := enhanceSummaryWithRecommendations(string(summaryData))
        if err != nil {
                log.Fatalf("Failed to enhance summary: %v", err)
        }

        // Write the enhanced summary to the output file
        err = os.WriteFile(outputFilePath, []byte(enhancedSummary), 0644)
        if err != nil {
                log.Fatalf("Failed to write output file: %v", err)
        }

        log.Printf("Enhanced summary with recommendations saved to %s", outputFilePath)
}

func enhanceSummaryWithRecommendations(summaryText string) (string, error) {
        // Prepare the chat API payload
        requestBody := map[string]interface{}{
                "model": modelName,
                "messages": []map[string]string{
                        {
                                "role": "system",
                                "content": "You are a system administrator assistant. Your task is to analyze log summaries, " +
                                        "create a concise meta-summary, and provide specific actionable recommendations to address " +
                                        "the issues found in the logs.",
                        },
                        {
                                "role": "user",
                                "content": fmt.Sprintf("Here is a summary of log analysis. Please create a shorter, " +
                                        "more concise summary of the key issues found, and then add a section called "+
                                        "\"RECOMMENDATIONS\" that lists specific, actionable steps to address the problems.\n\n%s",
                                        summaryText),
                        },
                },
                "temperature": 0.3, // Lower temperature for more consistent, focused responses
        }

        requestJSON, err := json.Marshal(requestBody)
        if err != nil {
                return "", fmt.Errorf("failed to create JSON payload: %v", err)
        }

        // Send the request to the AI model
        log.Println("Sending request to AI service...")
        resp, err := http.Post(aiEndpoint, "application/json", bytes.NewBuffer(requestJSON))
        if err != nil {
                return "", fmt.Errorf("failed to send request: %v", err)
        }
        defer resp.Body.Close()

        // Read the response
        body, err := io.ReadAll(resp.Body)
        if err != nil {
                return "", fmt.Errorf("failed to read response: %v", err)
        }

        // Extract the enhanced summary from the response
        var result map[string]interface{}
        err = json.Unmarshal(body, &result)
        if err != nil {
                return "", fmt.Errorf("failed to parse response: %v", err)
        }

        // Check for errors first
        if errorObj, hasError := result["error"].(map[string]interface{}); hasError {
                errorMsg := "Unknown error"
                if msg, ok := errorObj["message"].(string); ok {
                        errorMsg = msg
                }
                return "", fmt.Errorf("error from AI service: %s", errorMsg)
        } else if errorStr, hasErrorStr := result["error"].(string); hasErrorStr {
                return "", fmt.Errorf("error from AI service: %s", errorStr)
        }

        // Extract the content from the response
        enhancedSummary := "No summary generated."
        if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
                if choice, ok := choices[0].(map[string]interface{}); ok {
                        if message, ok := choice["message"].(map[string]interface{}); ok {
                                if content, ok := message["content"].(string); ok {
                                        enhancedSummary = content
                                }
                        }
                }
        }

        // Format the enhanced summary
        var buffer strings.Builder
        buffer.WriteString("# ENHANCED LOG SUMMARY WITH RECOMMENDATIONS\n")
        buffer.WriteString(fmt.Sprintf("Generated on %s\n\n", time.Now().Format(time.RFC1123)))
        buffer.WriteString(enhancedSummary)

        // Ensure there's a recommendations section if the LLM didn't add one
        if !strings.Contains(strings.ToUpper(enhancedSummary), "RECOMMENDATION") {
                buffer.WriteString("\n\n## RECOMMENDATIONS\n\n")
                buffer.WriteString("The AI did not provide specific recommendations. Please review the summary to determine appropriate actions.\n")
        }

        return buffer.String(), nil
}
