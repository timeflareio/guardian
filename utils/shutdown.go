package utils

import (
	"context"
	"sync"
)

// Shutdownable represents a service that can be gracefully shut down
type Shutdownable interface {
	Stop(ctx context.Context) error
}

// GracefulShutdown performs graceful shutdown of multiple services
func GracefulShutdown(ctx context.Context, services ...Shutdownable) error {
	if len(services) == 0 {
		return nil
	}

	var wg sync.WaitGroup
	errorChan := make(chan error, len(services))

	// Shutdown all services concurrently
	for _, service := range services {
		wg.Add(1)
		go func(s Shutdownable) {
			defer wg.Done()
			if err := s.Stop(ctx); err != nil {
				errorChan <- err
			}
		}(service)
	}

	// Wait for all services to shut down or timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All services shut down successfully
		close(errorChan)

		// Check if there were any errors
		for err := range errorChan {
			if err != nil {
				return err
			}
		}
		return nil

	case <-ctx.Done():
		// Timeout occurred
		return ctx.Err()
	}
}
