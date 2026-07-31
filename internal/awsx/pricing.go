package awsx

import (
	"strconv"
	"strings"

	"github.com/meisfrancis/ecis/internal/model"
)

// FargateRate is the on-demand price of one vCPU-hour and one GB-hour.
type FargateRate struct {
	VCPUHour float64 `yaml:"vcpuHour"`
	GBHour   float64 `yaml:"gbHour"`
}

// HoursPerMonth is the convention AWS uses when quoting monthly prices.
const HoursPerMonth = 730

// fargateRates holds indicative AWS Fargate on-demand rates for Linux/x86, in
// USD. They drive the local estimator only — the cost views prefer real Cost
// Explorer figures whenever they are available, and fall back to these when an
// account has not activated the cost allocation tags that make a per-service
// breakdown possible.
//
// Published prices change and vary by architecture (Graviton is cheaper),
// operating system (Windows is dearer) and purchase option (Spot, Compute
// Savings Plans). Treat estimates as an order-of-magnitude guide, and override
// them under cost.rates in the config file when precision matters.
var fargateRates = map[string]FargateRate{
	"us-east-1":      {0.04048, 0.004445},
	"us-east-2":      {0.04048, 0.004445},
	"us-west-1":      {0.04656, 0.005110},
	"us-west-2":      {0.04048, 0.004445},
	"ca-central-1":   {0.04456, 0.004892},
	"sa-east-1":      {0.08838, 0.009670},
	"eu-west-1":      {0.04456, 0.004865},
	"eu-west-2":      {0.04532, 0.004975},
	"eu-west-3":      {0.04512, 0.004950},
	"eu-central-1":   {0.04656, 0.005110},
	"eu-north-1":     {0.04302, 0.004723},
	"eu-south-1":     {0.04500, 0.004940},
	"ap-east-1":      {0.05376, 0.005900},
	"ap-south-1":     {0.04048, 0.004445},
	"ap-northeast-1": {0.05056, 0.005530},
	"ap-northeast-2": {0.04456, 0.004895},
	"ap-southeast-1": {0.05012, 0.005500},
	"ap-southeast-2": {0.04856, 0.005320},
	"me-south-1":     {0.04500, 0.004940},
	"af-south-1":     {0.05060, 0.005560},
}

// defaultFargateRate stands in for regions missing from the table.
var defaultFargateRate = FargateRate{0.04048, 0.004445}

// RateFor returns the Fargate rate for a region, honouring user overrides.
func RateFor(region string, overrides map[string]FargateRate) FargateRate {
	if r, ok := overrides[region]; ok && r.VCPUHour > 0 {
		return r
	}
	if r, ok := fargateRates[region]; ok {
		return r
	}
	return defaultFargateRate
}

// TaskCostPerHour estimates the hourly Fargate cost of one task from its
// reserved CPU units and memory. Tasks that are not Fargate, or that carry no
// size (EC2 launch type sizes at the container level), cost nothing here — the
// underlying EC2 capacity is billed separately and shows up in Cost Explorer
// under EC2 rather than ECS.
func TaskCostPerHour(t model.Task, rate FargateRate) float64 {
	if !isFargate(t) {
		return 0
	}
	vcpu := parseCPUUnits(t.CPU) / 1024
	memGB := parseMemoryMiB(t.Memory) / 1024
	if vcpu <= 0 && memGB <= 0 {
		return 0
	}
	return vcpu*rate.VCPUHour + memGB*rate.GBHour
}

func isFargate(t model.Task) bool {
	if strings.EqualFold(t.LaunchType, "FARGATE") {
		return true
	}
	// Capacity-provider tasks report an empty launch type; the provider name is
	// FARGATE or FARGATE_SPOT for serverless capacity.
	return strings.HasPrefix(strings.ToUpper(t.CapacityProv), "FARGATE")
}

// parseCPUUnits reads an ECS CPU value, which is either a unit count ("512") or
// a vCPU string (".5 vCPU", "1 vcpu").
func parseCPUUnits(s string) float64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "vcpu") {
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "vcpu")), 64)
		if err != nil {
			return 0
		}
		return v * 1024
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseMemoryMiB reads an ECS memory value, which is either MiB ("1024") or a
// sized string ("1GB", "2 gb").
func parseMemoryMiB(s string) float64 {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0
	}
	switch {
	case strings.HasSuffix(s, "gb"):
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "gb")), 64)
		if err != nil {
			return 0
		}
		return v * 1024
	case strings.HasSuffix(s, "mb"):
		v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "mb")), 64)
		if err != nil {
			return 0
		}
		return v
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
