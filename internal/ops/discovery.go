package ops

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/richardsondx/IronLark/internal/graph"
)

func ResolveEntity(snapshot graph.GraphSnapshot, query string) (LogicalEntity, error) {
	query = strings.TrimSpace(strings.ToLower(query))
	type scored struct {
		entity LogicalEntity
		score  int
	}
	candidates := []scored{}

	add := func(entity LogicalEntity, score int) {
		if score <= 0 {
			return
		}
		entity.ResolutionScore = score
		candidates = append(candidates, scored{entity: entity, score: score})
	}

	for _, service := range snapshot.Services {
		score := matchScore(query, service.Name, strings.TrimSuffix(service.Name, ".service"))
		if query == "" {
			score = 1
		}
		entity := LogicalEntity{
			ID:          "service:systemd:" + service.Name,
			Kind:        EntityService,
			Name:        service.Name,
			DisplayName: service.Name,
			Manager:     ManagerSystemd,
			Unit:        service.Name,
		}
		entity.ExpectedPorts = portsForService(snapshot.Relations, service.Name)
		entity.Port = firstPort(entity.ExpectedPorts)
		entity.HealthURL = inferHealthURL(entity, snapshot)
		add(entity, score+1000)
	}

	for _, container := range snapshot.Containers {
		score := matchScore(query, container.Name, container.Image, container.ID)
		if query == "" {
			score = 1
		}
		entity := LogicalEntity{
			ID:          "container:docker:" + firstNonEmpty(container.Name, container.ID),
			Kind:        EntityContainer,
			Name:        firstNonEmpty(container.Name, container.ID),
			DisplayName: firstNonEmpty(container.Name, container.ID),
			Manager:     ManagerDocker,
			Container:   firstNonEmpty(container.Name, container.ID),
			Aliases:     compactAliases(container.ID, container.Image),
		}
		entity.ExpectedPorts = portsForContainer(container.Ports)
		entity.Port = firstPort(entity.ExpectedPorts)
		entity.HealthURL = inferHealthURL(entity, snapshot)
		add(entity, score+500)
	}

	for _, listener := range snapshot.Listeners {
		line := fmt.Sprintf("%s:%d %s", listener.Address, listener.Port, listener.Process)
		score := matchScore(query, line, listener.Process, strconv.Itoa(listener.Port))
		if query == "" {
			score = 1
		}
		display := fmt.Sprintf("%s:%d", firstNonEmpty(listener.Address, "127.0.0.1"), listener.Port)
		entity := LogicalEntity{
			ID:            fmt.Sprintf("app:tcp:%s:%d", firstNonEmpty(listener.Process, "unknown"), listener.Port),
			Kind:          EntityApp,
			Name:          display,
			DisplayName:   display,
			Manager:       ManagerPort,
			Address:       firstNonEmpty(listener.Address, "127.0.0.1"),
			Port:          listener.Port,
			ExpectedPorts: []int{listener.Port},
			ObserveOnly:   true,
			Aliases:       compactAliases(listener.Process),
		}
		entity.HealthURL = inferHealthURL(entity, snapshot)
		add(entity, score)
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].entity.DisplayName < candidates[j].entity.DisplayName
		}
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) == 0 {
		return LogicalEntity{}, fmt.Errorf("no matching service, container, or app found for %q", query)
	}
	entity := candidates[0].entity
	if len(entity.ExpectedPorts) == 0 && entity.Port > 0 {
		entity.ExpectedPorts = []int{entity.Port}
	}
	if entity.Manager == ManagerPort {
		entity.ObserveOnly = true
	}
	return entity, nil
}

func inferHealthURL(entity LogicalEntity, snapshot graph.GraphSnapshot) string {
	if entity.Port <= 0 {
		return ""
	}
	name := strings.ToLower(entity.DisplayName + " " + entity.Name + " " + strings.Join(entity.Aliases, " "))
	if strings.Contains(name, "db") || strings.Contains(name, "redis") || strings.Contains(name, "postgres") || strings.Contains(name, "mysql") {
		return ""
	}
	if entity.Port == 80 || entity.Port == 443 || entity.Port == 3000 || entity.Port == 3001 || entity.Port == 8000 || entity.Port == 8080 {
		return fmt.Sprintf("http://127.0.0.1:%d/healthz", entity.Port)
	}
	for _, relation := range snapshot.Relations {
		if relation.From == "service:"+entity.Name && relation.Type == "listens_on" {
			return fmt.Sprintf("http://127.0.0.1:%d/healthz", entity.Port)
		}
	}
	return ""
}

func portsForService(relations []graph.GraphRelation, service string) []int {
	ports := []int{}
	for _, relation := range relations {
		if relation.From != "service:"+service || relation.Type != "listens_on" {
			continue
		}
		if port := parsePortFromRelation(relation.To); port > 0 {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return dedupeInts(ports)
}

func portsForContainer(raw string) []int {
	re := regexp.MustCompile(`(\d+)(?:/\w+)?`)
	matches := re.FindAllStringSubmatch(raw, -1)
	ports := []int{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		port, _ := strconv.Atoi(match[1])
		if port > 0 {
			ports = append(ports, port)
		}
	}
	sort.Ints(ports)
	return dedupeInts(ports)
}

func parsePortFromRelation(value string) int {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "port:") {
		return 0
	}
	port, _ := strconv.Atoi(strings.TrimPrefix(value, "port:"))
	return port
}

func matchScore(query string, values ...string) int {
	if strings.TrimSpace(query) == "" {
		return 0
	}
	score := 0
	for _, raw := range values {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value == "" {
			continue
		}
		switch {
		case value == query:
			score = maxInt(score, 100)
		case strings.TrimSuffix(value, ".service") == query:
			score = maxInt(score, 95)
		case strings.Contains(value, query):
			score = maxInt(score, 60)
		case strings.Contains(query, value):
			score = maxInt(score, 40)
		}
	}
	return score
}

func compactAliases(values ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func firstPort(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func dedupeInts(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	out := values[:0]
	last := -1
	for _, value := range values {
		if len(out) == 0 || value != last {
			out = append(out, value)
			last = value
		}
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
