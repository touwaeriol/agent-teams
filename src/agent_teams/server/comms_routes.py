"""Questions, approvals, mail and the plan of a conversation."""

from __future__ import annotations

from typing import Literal

from fastapi import APIRouter, Depends, HTTPException, Request, status

from ..comms import QuestionClosedError
from .auth import current_user
from .hub import NotAnswerable, UnknownQuestion
from .models import MailView, NewAnswer, PlanView, QuestionView, UserView
from .platform import platform

User = Depends(current_user)


def router() -> APIRouter:
    routes = APIRouter(prefix="/api/conversations/{conversation_id}")
    routes.add_api_route("/questions", list_questions, methods=["GET"])
    routes.add_api_route("/questions/{question_id}/answer", answer, methods=["POST"])
    routes.add_api_route("/mail", list_mail, methods=["GET"])
    routes.add_api_route("/plan", get_plan, methods=["GET"])
    return routes


async def list_questions(
    request: Request,
    conversation_id: str,
    user: UserView = User,
    status: Literal["open", "all"] = "open",
) -> list[QuestionView]:
    return await platform(request).hub.questions(user.id, conversation_id, status)


async def answer(
    request: Request, conversation_id: str, question_id: str, body: NewAnswer, user: UserView = User
) -> QuestionView:
    text = body.answer.strip()
    if not text:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, "回答不能为空")
    try:
        return await platform(request).hub.answer(user.id, conversation_id, question_id, text)
    except UnknownQuestion:
        raise HTTPException(status.HTTP_404_NOT_FOUND, "没有这个问题") from None
    except (NotAnswerable, QuestionClosedError) as exc:
        raise HTTPException(status.HTTP_409_CONFLICT, str(exc)) from exc
    except ValueError as exc:
        raise HTTPException(status.HTTP_422_UNPROCESSABLE_CONTENT, str(exc)) from exc


async def list_mail(
    request: Request, conversation_id: str, user: UserView = User
) -> list[MailView]:
    return await platform(request).hub.mail(user.id, conversation_id)


async def get_plan(request: Request, conversation_id: str, user: UserView = User) -> PlanView:
    return await platform(request).hub.plan(user.id, conversation_id)
