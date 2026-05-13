<h1 align="center" style="border-bottom: none;">Hanzo Visor</h1>
<h3 align="center">Cloud operating-system management platform (Go + React).</h3>

## Architecture

Hanzo Visor has two parts:

| Name     | Description                          | Language               | Source code                                |
|----------|--------------------------------------|------------------------|--------------------------------------------|
| Frontend | Web UI                               | JavaScript + React     | https://github.com/hanzoai/visor/tree/main/web |
| Backend  | RESTful API + Beego                  | Go + Beego + Postgres  | https://github.com/hanzoai/visor           |

## Installation

Hanzo Visor uses Hanzo IAM as the authentication system. Create an organization and an application for Visor in your IAM instance, then wire it via `app.conf`.

### Necessary configuration

#### Get the code

```shell
git clone https://github.com/hanzoai/iam
git clone https://github.com/hanzoai/visor
```

#### Setup database

Visor stores users, nodes, and resource information in a Postgres database named `visor` (auto-created). The DB connection string is configured in `conf/app.conf`:

```ini
dataSourceName = postgres://user:pass@localhost:5432/visor
```

#### Configure IAM

After creating an organization and an application in Hanzo IAM, update `clientID`, `clientSecret`, `iamOrganization`, and `iamApplication` in `app.conf`.

#### Run

```shell
go run main.go
```

Open browser: http://localhost:16001/

### Optional

#### RDP

Run guacd for RDP connections:

```shell
docker run --name guacd -d -p 4822:4822 guacamole/guacd
```

## License

[Apache-2.0](LICENSE)
