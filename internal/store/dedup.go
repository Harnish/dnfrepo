package store

import "context"

// PackageLoc is a package row with its repo slug, enough to find the file on disk.
type PackageLoc struct {
	RepoID         int64
	Slug, Filename string
	SHA256         string
	Size           int64
}

const locCols = `p.repo_id, r.slug, p.filename, p.sha256, p.size`

func (s *Store) queryLocs(ctx context.Context, q string, args ...any) ([]PackageLoc, error) {
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PackageLoc
	for rows.Next() {
		var l PackageLoc
		if err := rows.Scan(&l.RepoID, &l.Slug, &l.Filename, &l.SHA256, &l.Size); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PackagesBySHA returns every package with this content (sha256 and size), oldest first.
func (s *Store) PackagesBySHA(ctx context.Context, sha string, size int64) ([]PackageLoc, error) {
	return s.queryLocs(ctx, `SELECT `+locCols+` FROM packages p JOIN repos r ON r.id=p.repo_id
		WHERE p.sha256=$1 AND p.size=$2 ORDER BY p.id`, sha, size)
}

// DuplicatePackages returns every package whose content (sha256, size) appears more than once,
// grouped by content, oldest first within a group.
func (s *Store) DuplicatePackages(ctx context.Context) ([]PackageLoc, error) {
	return s.queryLocs(ctx, `SELECT `+locCols+` FROM packages p JOIN repos r ON r.id=p.repo_id
		WHERE (p.sha256, p.size) IN (SELECT sha256, size FROM packages GROUP BY sha256, size HAVING count(*) > 1)
		ORDER BY p.sha256, p.size, p.id`)
}
