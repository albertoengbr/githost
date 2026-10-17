package cli

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/config"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

// instance bundles the services CLI commands operate on. These commands run
// on the operator's machine with local access; authentication is a web/API
// concern (F2+).
type instance struct {
	cfg   *config.Config
	db    *sql.DB
	st    *store.Store
	gitx  *git.Executor
	svc   *reposervice.Service
	perms *permissions.Service
}

func (in *instance) Close() {
	in.db.Close()
}

// openInstance loads the configuration, opens and migrates the database and
// wires the service graph.
func openInstance(ctx context.Context, cfgPath string) (*instance, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	db, err := database.Open(ctx, cfg.Database.SQLite.Path)
	if err != nil {
		return nil, err
	}
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		db.Close()
		return nil, err
	}
	executor, err := git.New(nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	repoStorage, err := storage.NewRepositories(filepath.Join(cfg.Paths.Data, "repositories"), executor)
	if err != nil {
		db.Close()
		return nil, err
	}
	st := store.New(db)
	return &instance{
		cfg:   cfg,
		db:    db,
		st:    st,
		gitx:  executor,
		svc:   reposervice.New(st, repoStorage),
		perms: permissions.New(st),
	}, nil
}

// ------------------------------------------------------------ admin command

func runAdmin(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		adminUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create-user":
		return runAdminCreateUser(ctx, rest, stdin, stdout, stderr)
	case "create-token":
		return runAdminCreateToken(ctx, rest, stdout, stderr)
	case "add-ssh-key":
		return runAdminAddSSHKey(ctx, rest, stdout, stderr)
	case "list-users":
		return runAdminListUsers(ctx, rest, stdout, stderr)
	case "--help", "-h", "help":
		adminUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "githost admin: unknown subcommand %q\n\n", sub)
		adminUsage(stderr)
		return 2
	}
}

func adminUsage(w io.Writer) {
	fmt.Fprint(w, `usage: githost admin <subcommand> [flags]

Subcommands:
  create-user    Create a user account (first user becomes an admin)
  create-token   Create an access token for a user
  add-ssh-key    Register a public SSH key for a user
  list-users     List user accounts
`)
}

func runAdminCreateUser(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlagSet("admin create-user", "githost admin create-user --username NAME [flags]", stderr)
	cfgPath := configFlag(fs)
	username := fs.String("username", "", "account name (lowercase letters, digits, . _ -)")
	email := fs.String("email", "", "account email (optional)")
	password := fs.String("password", "", "password (testing convenience; prefer --password-stdin)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from standard input")
	isAdmin := fs.Bool("admin", false, "make this account an instance administrator")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	*username = store.NormalizeUsername(*username)
	if err := store.ValidateUsername(*username); err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	var plain string
	switch {
	case *passwordStdin:
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(stderr, "githost: reading password from stdin:", err)
			return 1
		}
		plain = strings.TrimSpace(line)
	case *password != "":
		plain = *password
	default:
		fmt.Fprintln(stderr, "githost: provide --password-stdin (recommended) or --password")
		return 2
	}
	if err := auth.ValidatePasswordPolicy(plain); err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	hash, err := auth.HashPassword(plain)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	// Bootstrap rule: the first account on a fresh instance becomes an
	// admin, whoever runs the command.
	count, err := in.st.CountUsers(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	makeAdmin := *isAdmin || count == 0
	user, err := in.st.CreateUser(ctx, *username, *email, hash, makeAdmin)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "created user %s (id=%d, admin=%v)\n", user.Username, user.ID, user.IsAdmin)
	if makeAdmin && !*isAdmin {
		fmt.Fprintln(stdout, "note: first account on this instance was made an administrator")
	}
	return 0
}

func runAdminCreateToken(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("admin create-token", "githost admin create-token --username NAME --scope SCOPE [flags]", stderr)
	cfgPath := configFlag(fs)
	username := fs.String("username", "", "account name")
	name := fs.String("name", "", "token label (e.g. \"ci\", \"laptop\")")
	var scopes []string
	fs.Func("scope", "token scope; repeatable (repo:read, repo:write, admin)", func(v string) error {
		scopes = append(scopes, v)
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *username == "" {
		fmt.Fprintln(stderr, "githost: --username is required")
		return 2
	}
	scopesStr := strings.Join(scopes, " ")
	if err := auth.ValidateScopes(scopesStr); err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	user, err := in.st.UserByUsername(ctx, *username)
	if err != nil {
		fmt.Fprintf(stderr, "githost: user %q not found\n", *username)
		return 1
	}
	token, tokenHash, prefix, err := auth.GenerateToken()
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	if _, err := in.st.CreateToken(ctx, user.ID, *name, tokenHash, prefix, scopesStr, nil); err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "token for %s (scopes: %s):\n  %s\n", user.Username, scopesStr, token)
	fmt.Fprintln(stdout, "store it now — it is shown only once")
	return 0
}

func runAdminAddSSHKey(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("admin add-ssh-key", "githost admin add-ssh-key --username NAME (--key LINE | --key-file FILE) [flags]", stderr)
	cfgPath := configFlag(fs)
	username := fs.String("username", "", "account name")
	key := fs.String("key", "", "public key in authorized_keys format")
	keyFile := fs.String("key-file", "", "path to a public key file")
	title := fs.String("title", "", "key label (e.g. \"laptop\")")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var rawKey string
	switch {
	case *keyFile != "":
		data, err := os.ReadFile(*keyFile)
		if err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
		rawKey = string(data)
	case *key != "":
		rawKey = *key
	default:
		fmt.Fprintln(stderr, "githost: provide --key or --key-file")
		return 2
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	user, err := in.st.UserByUsername(ctx, *username)
	if err != nil {
		fmt.Fprintf(stderr, "githost: user %q not found\n", *username)
		return 1
	}
	canonical, fingerprint, err := auth.ParseSSHPublicKey(rawKey)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	if _, err := in.st.AddSSHKey(ctx, user.ID, fingerprint, canonical, *title); err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "added key %s for %s\n", fingerprint, user.Username)
	return 0
}

func runAdminListUsers(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("admin list-users", "githost admin list-users [flags]", stderr)
	cfgPath := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	users, err := in.st.Users(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "ID\tUSERNAME\tADMIN")
	for _, u := range users {
		fmt.Fprintf(stdout, "%d\t%s\t%v\n", u.ID, u.Username, u.IsAdmin)
	}
	return 0
}

// ------------------------------------------------------------- repo command

func runRepo(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		repoUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return runRepoCreate(ctx, rest, stdout, stderr)
	case "list":
		return runRepoList(ctx, rest, stdout, stderr)
	case "add-member":
		return runRepoMember(ctx, true, rest, stdout, stderr)
	case "remove-member":
		return runRepoMember(ctx, false, rest, stdout, stderr)
	case "delete":
		return runRepoDelete(ctx, rest, stdout, stderr)
	case "--help", "-h", "help":
		repoUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "githost repo: unknown subcommand %q\n\n", sub)
		repoUsage(stderr)
		return 2
	}
}

func repoUsage(w io.Writer) {
	fmt.Fprint(w, `usage: githost repo <subcommand> [flags]

Subcommands:
  create         Create a repository (private by default)
  list           List repositories (all, or filtered by --owner)
  add-member     Grant a user access to a repository
  remove-member  Revoke a user's access
  delete         Delete a repository and its data (requires --confirm)
`)
}

func runRepoCreate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("repo create", "githost repo create --owner NAME --name NAME [flags]", stderr)
	cfgPath := configFlag(fs)
	owner := fs.String("owner", "", "owner username")
	name := fs.String("name", "", "repository name")
	visibility := fs.String("visibility", store.VisibilityPrivate, "private or public")
	description := fs.String("description", "", "repository description")
	defaultBranch := fs.String("default-branch", "main", "default branch name")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *owner == "" || *name == "" {
		fmt.Fprintln(stderr, "githost: --owner and --name are required")
		return 2
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	ownerUser, err := in.st.UserByUsername(ctx, *owner)
	if err != nil {
		fmt.Fprintf(stderr, "githost: user %q not found\n", *owner)
		return 1
	}
	repo, err := in.svc.Create(ctx, ownerUser, *name, reposervice.CreateOptions{
		Description:   *description,
		Visibility:    *visibility,
		DefaultBranch: *defaultBranch,
	})
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "created %s/%s (%s, uuid %s)\n", repo.Owner, repo.Name, repo.Visibility, repo.UUID)
	return 0
}

func runRepoList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("repo list", "githost repo list [--owner NAME]", stderr)
	cfgPath := configFlag(fs)
	owner := fs.String("owner", "", "filter by owner username")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	var repos []*store.Repository
	if *owner != "" {
		ownerUser, err := in.st.UserByUsername(ctx, *owner)
		if err != nil {
			fmt.Fprintf(stderr, "githost: user %q not found\n", *owner)
			return 1
		}
		repos, err = in.svc.ListByOwner(ctx, ownerUser.ID)
		if err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
	} else {
		repos, err = in.svc.ListAll(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
	}
	fmt.Fprintln(stdout, "OWNER\tNAME\tVISIBILITY\tARCHIVED\tUUID")
	for _, r := range repos {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%v\t%s\n", r.Owner, r.Name, r.Visibility, r.IsArchived, r.UUID)
	}
	return 0
}

func runRepoMember(ctx context.Context, add bool, args []string, stdout, stderr io.Writer) int {
	action := "add-member"
	if !add {
		action = "remove-member"
	}
	fs := newFlagSet("repo "+action, "githost repo "+action+" --owner NAME --name NAME --user NAME [--permission P]", stderr)
	cfgPath := configFlag(fs)
	owner := fs.String("owner", "", "owner username")
	name := fs.String("name", "", "repository name")
	user := fs.String("user", "", "member username")
	permission := fs.String("permission", store.PermissionRead, "member permission: read, write or admin")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *owner == "" || *name == "" || *user == "" {
		fmt.Fprintln(stderr, "githost: --owner, --name and --user are required")
		return 2
	}
	if add {
		if err := store.ValidateMemberPermission(*permission); err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	repo, err := in.svc.Resolve(ctx, *owner, *name)
	if err != nil {
		fmt.Fprintf(stderr, "githost: repository %s/%s not found\n", *owner, *name)
		return 1
	}
	member, err := in.st.UserByUsername(ctx, *user)
	if err != nil {
		fmt.Fprintf(stderr, "githost: user %q not found\n", *user)
		return 1
	}
	if member.ID == repo.OwnerID {
		fmt.Fprintln(stderr, "githost: the owner already has admin access")
		return 1
	}
	if add {
		if err := in.st.AddMember(ctx, repo.ID, member.ID, *permission); err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s is now a member of %s/%s (%s)\n", member.Username, repo.Owner, repo.Name, *permission)
	} else {
		if err := in.st.RemoveMember(ctx, repo.ID, member.ID); err != nil {
			fmt.Fprintf(stderr, "githost: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s is no longer a member of %s/%s\n", member.Username, repo.Owner, repo.Name)
	}
	return 0
}

func runRepoDelete(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("repo delete", "githost repo delete --owner NAME --name NAME --confirm", stderr)
	cfgPath := configFlag(fs)
	owner := fs.String("owner", "", "owner username")
	name := fs.String("name", "", "repository name")
	confirm := fs.Bool("confirm", false, "required: acknowledge the deletion of all repository data")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*confirm {
		fmt.Fprintln(stderr, "githost: refusing to delete without --confirm")
		return 2
	}

	in, err := openInstance(ctx, *cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	defer in.Close()

	repo, err := in.svc.Resolve(ctx, *owner, *name)
	if err != nil {
		fmt.Fprintf(stderr, "githost: repository %s/%s not found\n", *owner, *name)
		return 1
	}
	if err := in.svc.Delete(ctx, repo); err != nil {
		fmt.Fprintf(stderr, "githost: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted %s/%s and its on-disk data\n", repo.Owner, repo.Name)
	return 0
}
