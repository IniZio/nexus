# Tasks

Tasks are the fundamental unit of work in Nexus.

## Creating Tasks

Create a new task:
```bash
nexus task create "Your task description"
```

## Managing Tasks

List all tasks:
```bash
nexus task list
```

View task details:
```bash
nexus task show <task-id>
```

Update task status:
```bash
nexus task update <task-id> --status in-progress
```

Delete a task:
```bash
nexus task delete <task-id>
```

## Task States

- **pending** - Task created but not started
- **in-progress** - Task is actively being worked on
- **review** - Task is under review
- **completed** - Task is finished
- **archived** - Task is archived
