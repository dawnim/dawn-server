"""本地OpenAI兼容向量端点；使用Sentence Transformers处理模型和归一化。"""
import os
import threading
from contextlib import asynccontextmanager

import torch
from fastapi import FastAPI, HTTPException
from huggingface_hub import scan_cache_dir
from pydantic import BaseModel, Field
from sentence_transformers import SentenceTransformer

MODEL = os.getenv("EMBEDDING_MODEL", "Qwen/Qwen3-Embedding-0.6B")
lock = threading.Lock()


@asynccontextmanager
async def lifespan(app: FastAPI):
    torch.set_num_threads(int(os.getenv("EMBEDDING_THREADS", "4")))
    app.state.model = SentenceTransformer(MODEL, device=os.getenv("EMBEDDING_DEVICE", "cpu"))
    app.state.model.max_seq_length = int(os.getenv("EMBEDDING_MAX_LENGTH", "2048"))
    app.state.cache_bytes = None
    app.state.model_bytes = None
    try:
        cache = scan_cache_dir(os.path.join(os.environ.get("HF_HOME", ""), "hub"))
        app.state.cache_bytes = cache.size_on_disk
        app.state.model_bytes = next((repo.size_on_disk for repo in cache.repos if repo.repo_id == MODEL), None)
    except OSError:
        pass
    yield


app = FastAPI(lifespan=lifespan)


@app.get("/health")
def health():
    return {"model": MODEL, "ready": True,
            "dimensions": app.state.model.get_sentence_embedding_dimension(),
            "device": str(app.state.model.device),
            "max_length": app.state.model.max_seq_length,
            "threads": torch.get_num_threads(), "model_bytes": app.state.model_bytes,
            "cache_bytes": app.state.cache_bytes}


class Request(BaseModel):
    model: str
    input: str | list[str]
    dimensions: int | None = Field(default=None, ge=1)
    encoding_format: str = "float"


@app.post("/v1/embeddings")
def embeddings(request: Request):
    if request.model != MODEL or request.encoding_format != "float":
        raise HTTPException(422, "Invalid embedding model or encoding format")
    maximum = app.state.model.get_sentence_embedding_dimension()
    if request.dimensions is not None and not 32 <= request.dimensions <= maximum:
        raise HTTPException(422, "Dimensions must be between 32 and the model dimension")
    texts = [request.input] if isinstance(request.input, str) else request.input
    if not texts or len(texts) > 32:
        raise HTTPException(422, "Expected 1 to 32 texts")
    with lock, torch.inference_mode():
        vectors = app.state.model.encode(texts, batch_size=1, normalize_embeddings=True,
                                        truncate_dim=request.dimensions, prompt="")
    return {"object": "list", "model": MODEL,
            "data": [{"object": "embedding", "index": index, "embedding": vector.tolist()}
                     for index, vector in enumerate(vectors)],
            "usage": {"prompt_tokens": 0, "total_tokens": 0}}
