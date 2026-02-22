# Nexus Workspace Guide: Python Data Science

This guide explains how to use the data science environment with Nexus workspaces.

## Quick Start with Nexus

### 1. Create the Workspace

```bash
# From the repository root
nexus workspace create datascience-demo \
  --display-name "Data Science Workspace" \
  --dind  # Enable Docker-in-Docker
```

### 2. Start the Services

```bash
# SSH into the workspace
nexus workspace console datascience-demo

# Navigate to the example
cd examples/python-datascience-workspace

# Start Jupyter and PostgreSQL
docker-compose up -d

# View logs to get the Jupyter token
docker-compose logs jupyter | grep token
```

### 3. Set Up Port Forwarding

From your **host machine** (in a new terminal):

```bash
# Forward Jupyter Lab (required)
nexus workspace port add datascience-demo 8888

# Forward PostgreSQL (optional, for external tools)
nexus workspace port add datascience-demo 5432

# Verify port forwards
nexus workspace port list datascience-demo
```

### 4. Access Jupyter Lab

1. Open your browser: **http://localhost:8888**
2. Enter the token from the logs (or use the URL with token)
3. Start exploring notebooks in the `notebooks/` directory

## Working with the Workspace

### Daily Workflow

```bash
# 1. Start workspace if needed
nexus workspace start datascience-demo

# 2. Forward port (if not already done)
nexus workspace port add datascience-demo 8888

# 3. Open browser to http://localhost:8888
# 4. Work in Jupyter Lab
```

### Running Python Scripts

Execute scripts without entering Jupyter:

```bash
# Run data generation script
nexus workspace exec datascience-demo -- docker-compose exec jupyter python data/generate_sample_data.py

# Run a specific notebook (convert to script first)
nexus workspace exec datascience-demo -- docker-compose exec jupyter jupyter nbconvert --to script notebooks/01-data-exploration.ipynb

# Execute the script
nexus workspace exec datascience-demo -- docker-compose exec jupyter python notebooks/01-data-exploration.py
```

### Database Operations

```bash
# Access PostgreSQL CLI
nexus workspace exec datascience-demo -- docker-compose exec postgres psql -U postgres -d datascience

# List tables
\dt

# Query data
SELECT COUNT(*) FROM customers;

# Exit
\q
```

## Checkpointing for Experiments

### Why Checkpoints Matter for Data Science

Data science work often involves long-running experiments. Checkpoints let you:
1. Save the state after data preprocessing (takes time)
2. Experiment with different models safely
3. Rollback if an experiment goes wrong

### Creating Checkpoints

```bash
# After initial setup with data loaded
nexus workspace checkpoint create datascience-demo --name "data-loaded-baseline"

# After feature engineering
nexus workspace checkpoint create datascience-demo --name "features-engineered-v1"

# Before training expensive model
nexus workspace checkpoint create datascience-demo --name "before-xgb-training"
```

### Experiment Workflow

```bash
# 1. Start from baseline
nexus workspace restore datascience-demo <data-loaded-baseline-id>

# 2. SSH in
nexus workspace console datascience-demo

# 3. Start services
docker-compose up -d

# 4. Run experiments...

# 5. Checkpoint if successful
nexus workspace checkpoint create datascience-demo --name "experiment-success"

# Or restore if failed
nexus workspace restore datascience-demo <before-experiment-id>
```

### Checkpoint Best Practices

**Checkpoint After:**
- Data loading and cleaning (can take hours)
- Feature engineering pipelines
- Successful model training
- Important analysis milestones

**Checkpoint Names:**
```bash
# Good - descriptive
nexus workspace checkpoint create datascience-demo --name "raw-data-imported-2024-01"
nexus workspace checkpoint create datascience-demo --name "features-normalized"

# Bad - not helpful
nexus workspace checkpoint create datascience-demo --name "checkpoint-1"
```

## Advanced Usage

### Connecting External Tools

With port 5432 forwarded, you can connect external database tools:

**TablePlus / DBeaver:**
- Host: `localhost`
- Port: `5432`
- User: `postgres`
- Password: `postgres`
- Database: `datascience`

**Python from Host:**
```python
import pandas as pd
from sqlalchemy import create_engine

# Connect through forwarded port
engine = create_engine('postgresql://postgres:postgres@localhost:5432/datascience')
df = pd.read_sql('SELECT * FROM customers', engine)
```

### Persistent Data Storage

Data in volumes survives across stop/start:

```bash
# Work with data
nexus workspace exec datascience-demo -- docker-compose exec jupyter python data/generate_sample_data.py

# Stop workspace
nexus workspace stop datascience-demo

# Start next day
nexus workspace start datascience-demo

# Data is still there!
nexus workspace exec datascience-demo -- docker-compose exec postgres psql -U postgres -d datascience -c "SELECT COUNT(*) FROM customers;"
```

### Installing Additional Packages

```bash
# SSH into workspace
nexus workspace console datascience-demo

# Install package in running container
docker-compose exec jupyter pip install transformers torch

# Or add to requirements.txt and rebuild
echo "transformers" >> requirements.txt
docker-compose build jupyter
docker-compose up -d
```

### Exporting Results

```bash
# Copy notebook outputs to host
nexus workspace exec datascience-demo -- docker-compose exec jupyter tar czf /tmp/results.tar.gz notebooks/ figures/

# Extract on host (from workspace directory)
cp /workspace/path/to/results.tar.gz ./
tar xzf results.tar.gz
```

## Workspace Lifecycle

### Pausing (Quick Context Switch)

When you need to switch contexts but want to preserve memory state:

```bash
nexus workspace pause datascience-demo

# Later...
nexus workspace resume datascience-demo
# Jupyter server still running!
```

### Stopping (Save Resources)

```bash
nexus workspace stop datascience-demo

# Start later
nexus workspace start datascience-demo
nexus workspace port add datascience-demo 8888
```

### Destroying (Cleanup)

```bash
# This removes everything including checkpoints
nexus workspace destroy datascience-demo

# To preserve data, export first!
```

## Troubleshooting

### Jupyter Token Lost

```bash
# Get new token
nexus workspace exec datascience-demo -- docker-compose logs jupyter | grep token

# Or restart to get fresh token
nexus workspace exec datascience-demo -- docker-compose restart jupyter
```

### Port Already in Use

```bash
# Use a different local port
nexus workspace port add datascience-demo 8889

# Then access: http://localhost:8889
```

### Out of Disk Space

```bash
# Check usage
nexus workspace exec datascience-demo -- df -h

# Clean Docker
nexus workspace exec datascience-demo -- docker system prune -f
```

### Kernel Won't Connect

```bash
# Restart Jupyter
docker-compose restart jupyter

# Or check logs
docker-compose logs jupyter
```

## Example Session

```bash
# Create workspace
nexus workspace create datascience-demo --dind

# Forward port
nexus workspace port add datascience-demo 8888

# SSH and setup
nexus workspace console datascience-demo
cd examples/python-datascience-workspace
docker-compose up -d
docker-compose exec jupyter python data/generate_sample_data.py

# Create checkpoint
nexus workspace checkpoint create datascience-demo --name "data-ready"

# Open browser to http://localhost:8888
# Work in Jupyter...

# Checkpoint progress
nexus workspace checkpoint create datascience-demo --name "eda-complete"

# Pause when done for day
nexus workspace pause datascience-demo

# Next morning
nexus workspace resume datascience-demo
# Jupyter still running, pick up where you left off!
```

## Best Practices

### 1. Use Checkpoints Strategically
- Before long-running operations
- After data cleaning
- At analysis milestones

### 2. Version Control Notebooks
```bash
# From workspace, commit notebooks
git add notebooks/
git commit -m "Complete EDA notebook"
```

### 3. Separate Data from Code
- Keep data in volumes (persistent)
- Commit notebooks and scripts to git
- Use `.gitignore` for large files

### 4. Document Your Environment
Always include `requirements.txt`:
```
pandas==2.1.0
numpy==1.24.0
scikit-learn==1.3.0
jupyterlab==4.0.0
```

### 5. Regular Checkpoints
Don't wait too long between checkpoints:
```bash
# Set a timer, checkpoint every 2 hours during intensive work
nexus workspace checkpoint create datascience-demo --name "checkpoint-$(date +%H%M)"
```

## See Also

- [Main README](./README.md) - Project overview
- [Jupyter Lab Documentation](https://jupyterlab.readthedocs.io/)
- [Pandas Documentation](https://pandas.pydata.org/docs/)
- [Scikit-learn Documentation](https://scikit-learn.org/stable/documentation.html)
