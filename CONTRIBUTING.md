# Contributing to DockWarden

Thank you for your interest in contributing to DockWarden! 🎉

## Code of Conduct

This project follows a code of conduct. By participating, you are expected to uphold this code. Please be respectful and constructive in all interactions.

## How to Contribute

### Reporting Bugs

1. Check if the bug has already been reported in [Issues](https://github.com/emon5122/dockwarden/issues)
2. If not, create a new issue with:
   - Clear title and description
   - Steps to reproduce
   - Expected vs actual behavior
   - DockWarden version
   - Docker version
   - Relevant logs

### Suggesting Features

1. Check existing issues and discussions
2. Create a new issue with the `enhancement` label
3. Describe the feature and why it's useful
4. Include examples if possible

### Pull Requests

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-feature`
3. Make your changes
4. Write/update tests
5. Run tests: `go test ./...`
6. Commit with a clear message
7. Push and create a Pull Request

## Development Setup

### Prerequisites

- Go 1.25.6+
- Docker
- Make (optional)

### Building

```bash
# Clone
git clone https://github.com/emon5122/dockwarden.git
cd dockwarden

# Build
go build -o dockwarden ./cmd/dockwarden

# Run tests
go test -v ./...

# Build Docker image
docker build -t dockwarden:dev -f build/Dockerfile .
```

### Running Locally

```bash
# With direct socket access
./dockwarden --interval 60s --log-level debug

# With Docker
docker run -v /var/run/docker.sock:/var/run/docker.sock dockwarden:dev
```

## Code Style

- Follow standard Go conventions
- Run `go fmt` before committing
- Use meaningful variable and function names
- Add comments for complex logic
- Write tests for new functionality

## Commit Messages

Use clear, descriptive commit messages:

```
feat: add Discord notification support
fix: handle empty container labels
docs: update configuration examples
test: add health watcher tests
refactor: simplify update logic
```

## Testing

- Write unit tests for new code
- Update existing tests if behavior changes
- Run the full test suite before submitting

```bash
go test -v -race ./...
```

## Documentation

- Update docs if adding/changing features
- Include examples in documentation
- Keep README up to date

## Questions?

Feel free to open an issue or start a discussion if you have questions!

Thank you for contributing! 🙏
