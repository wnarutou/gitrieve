package typedef

type Repository struct {
	Name               string   `yaml:"name"`
	URL                string   `yaml:"url"`
	Cron               string   `yaml:"cron"`
	Storage            []string `yaml:"storage"`
	UseCache           bool     `yaml:"useCache"`
	Type               string   `yaml:"type"` // repo, user, org (default: repo)
	OrgName            string   `yaml:"orgName"`
	AllBranches        bool     `yaml:"allBranches"`        // pull all branches or not (default: false)
	Depth              int      `yaml:"depth"`              // pull depth: 0, 1, ... (default: 0, means all commit logs)
	DownloadReleases   bool     `yaml:"downloadReleases"`   // download releases or not (default: false)
	DownloadIssues     bool     `yaml:"downloadIssues"`     // download issues or not (default: false)
	DownloadWiki       bool     `yaml:"downloadWiki"`       // download wiki or not (default: false)
	DownloadDiscussion bool     `yaml:"downloadDiscussion"` // download discussion or not (default: false)
}

func (r *Repository) GetType() string {
	// backward compatibility, default to repo
	if r.Type == "" {
		return TypeRepo
	}
	return r.Type
}
