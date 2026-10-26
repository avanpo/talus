# talus

`talus` is a self-hosted endurance telemetry backup and training analytics engine designed for trail and road running, gravel cycling, and mountain biking.

It runs continuously as a lightweight service on your home server, incrementally synchronizing second-by-second activity telemetry from Strava, tracking edits and crops, and serving an interactive web dashboard with volume rollups and physiological progress analytics (Aerobic Efficiency Factor, Decoupling, and Route Progression).

---

## Quick Setup (Docker / Self-Hosted)

### 1. Prerequisites: Strava API Credentials
1. Go to [strava.com/settings/api](https://www.strava.com/settings/api) and create an API application.
2. Set the **Authorization Callback Domain** to `localhost`.
3. Note your **Client ID** and **Client Secret**.

---

### 2. Run the Container

#### With `docker run`:
```bash
docker run -d \
  --name talus \
  --restart unless-stopped \
  -p 8080:8080 \
  -v ~/talus-data:/data \
  talus
```

#### Or with `docker-compose.yml`:
```yaml
services:
  talus:
    image: talus:latest
    container_name: talus
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ~/talus-data:/data
    environment:
      - PORT=8080
      - SYNC_INTERVAL=1h              # Hourly incremental backup
      - CHECK_UPDATES_INTERVAL=24h     # Daily scan for title edits, crops, gear changes
```

---

### 3. One-Click Web Authorization
1. Open `http://<your-server-ip>:8080` in your browser.
2. If this is a fresh setup, Talus will automatically show the **Connect with Strava** setup page.
3. Enter your **Client ID** and **Client Secret** and click **Connect**.
4. Once authorized, Talus saves `.talus-config.json` into your persistent directory, begins the initial sync in the background, and loads your dashboard.

---

## Local / Headless CLI Usage

You can also run Talus directly without Docker:

```bash
# Run web dashboard & background daemon:
go run . /path/to/data

# Run a one-off headless sync (prompts for setup if unconfigured):
go run . sync /path/to/data

# Scan recent activities for Strava edits / title changes / crops:
go run . sync /path/to/data --recent 30d

# Force re-download all activities:
go run . sync /path/to/data --force
```

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | HTTP port for the web dashboard and API. |
| `SYNC_INTERVAL` | `1h` | Interval between incremental sync checks for new activities. |
| `CHECK_UPDATES_INTERVAL` | `24h` | Interval between scans detecting Strava edits, crops, and gear changes. |
| `DATA_DIR` | `/data` | Path to persistent directory holding `.talus-config.json` and activities. |
| `STRAVA_CLIENT_ID` | `""` | Optional: prefill Client ID in setup page. |
| `STRAVA_CLIENT_SECRET` | `""` | Optional: prefill Client Secret in setup page. |

---

## Web Dashboard Views

### 1. Volume Statistics (`/`)
- **Period Rollups:** Weekly, Monthly, and Yearly aggregation.
- **Sport Breakdown:** Filter by Running, Trail, Road, Rides (Gravel/MTB), Hikes, or All Activities.
- **Visual Charts:** Bar chart of distance with stacked Road vs. Trail splits and elevation gain line overlay.
- **Pace & GAP:** Trend lines comparing flat pace with Grade-Adjusted Pace.
- **Units:** Toggle dynamically between Metric (`km`, `m`) and Imperial (`mi`, `ft`).

### 2. Fitness & Progress (`/progress`)
- **Cardiovascular Efficiency Factor (EF):** Tracks meters moved per heartbeat (`Speed / Avg HR`). Scatter plot with a 5-run rolling average trendline to identify aerobic conditioning gains.
- **Aerobic Decoupling ($Pa:Hr$):** Evaluates cardiac drift between the 1st half and 2nd half of steady-state runs (target $< 5\%$).
- **Repeated Route Progressions:** Automatically clusters activities matching the same GPS route and tracks date-by-date pace, HR, and efficiency improvements over identical terrain.
