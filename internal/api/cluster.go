package api

import (
	"devops-lab-backend/internal/k8s"
	"encoding/json"
	"net/http"
)

// CreateClusterHandler initiates manual cluster creation (Admin only)
// POST /api/cluster/create
func CreateClusterHandler(k8sClient *k8s.K8sClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k8sClient == nil || k8sClient.ClusterMgr == nil {
			http.Error(w, "K8s infrastructure is not initialized", http.StatusServiceUnavailable)
			return
		}

		err := k8sClient.ClusterMgr.ManualCreateCluster(r.Context())
		if err != nil {
			http.Error(w, "Failed to initiate cluster creation: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Cluster creation initiated successfully",
		})
	}
}

// DeleteClusterHandler initiates manual cluster deletion (Admin only)
// POST /api/cluster/delete
func DeleteClusterHandler(k8sClient *k8s.K8sClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k8sClient == nil || k8sClient.ClusterMgr == nil {
			http.Error(w, "K8s infrastructure is not initialized", http.StatusServiceUnavailable)
			return
		}

		err := k8sClient.ClusterMgr.ManualDeleteClusters(r.Context())
		if err != nil {
			http.Error(w, "Failed to initiate cluster deletion: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "success",
			"message": "Cluster deletion initiated successfully",
		})
	}
}

// GetClusterStatusHandler returns the current status of the GKE cluster (Admin only)
// GET /api/cluster/status
func GetClusterStatusHandler(k8sClient *k8s.K8sClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k8sClient == nil || k8sClient.ClusterMgr == nil {
			http.Error(w, "K8s infrastructure is not initialized", http.StatusServiceUnavailable)
			return
		}

		name, status, err := k8sClient.ClusterMgr.GetClusterStatus(r.Context())
		if err != nil {
			http.Error(w, "Failed to get cluster status: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"name":   name,
			"status": status,
		})
	}
}
