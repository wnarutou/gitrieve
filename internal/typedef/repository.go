package typedef

type Repository struct {
	Name        string   `yaml:"name"`
	URL         string   `yaml:"url"`
	Cron        string   `yaml:"cron"`
	Storage     []string `yaml:"storage"`
	UseCache    bool     `yaml:"useCache"`
	Type        string   `yaml:"type"` // repo, user, org (default: repo)
	OrgName     string   `yaml:"orgName"`
	AllBranches bool     `yaml:"allBranches"` // pull all branches or not (default: false)
	Depth       int      `yaml:"depth"`       // pull depth: 0, 1, ... (default: 0, means all commit logs)
}

func (r *Repository) GetType() string {
	// backward compatibility, default to repo
	if r.Type == "" {
		return TypeRepo
	}
	return r.Type
}
