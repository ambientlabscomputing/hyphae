package service

import "context"

func (s *AppService) Health(ctx context.Context) (*map[string]interface{}, error) {
	return s.repo.Health(ctx)
}
